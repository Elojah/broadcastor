package store

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"iter"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/elojah/multicast/subscriber"
)

// ErrTooLarge is what File.Put returns for a record larger than WithMaxSize.
var ErrTooLarge = errors.New("record too large")

// DecodeError is what File.Next returns for an entry it cannot read back, mostly one decode rejects. The entry stays:
// Ack(ID) skips it.
type DecodeError struct {
	ID  string
	Err error
}

func (e *DecodeError) Error() string {
	return "entry " + e.ID + ": " + e.Err.Error()
}

func (e *DecodeError) Unwrap() error {
	return e.Err
}

// File is a Queue on local disk, for store and forward across restarts. It appends entries to segment files in a
// directory, each with a checksum, so that OpenFile drops a record a power cut tore, and keeps where the oldest entry
// not acked is in a file of its own. A power cut loses what was not synced yet (WithSyncEvery, WithSyncInterval), and
// hands again what was acked since the last sync: delivery is at least once.
//
// A record's error is read back with its text, and matches the subscriber sentinels the original matched
// (subscriber.ErrTimeout...), but no other error. Once WithMaxSize is reached, Put drops the oldest segment, even the
// entry Next returned, and counts its entries (Dropped).
//
// Create one with OpenFile, and Close it. A directory takes one File at a time. Put may be called concurrently, but
// Next and Replay are its single reader.
type File[T any] struct {
	dir    string
	encode func(T) ([]byte, error)
	decode func([]byte) (T, error)
	config fileConfig
	fsync  func(*os.File) error

	mu sync.Mutex

	// segments, oldest first, hold every entry not acked. Put appends to the newest once it has opened it in w, which
	// is the case only after its first Put: a File never appends to a segment from before OpenFile.
	segments []segment
	w        *os.File
	r        *os.File // the oldest segment, for Next, opened with rBase its base
	rBase    int64

	head    int64 // position of the oldest entry not acked, tail if there is none
	headEnd int64 // where the record at head ends, once Next has read it
	tail    int64 // position the next record goes at
	n       int   // entries not acked
	size    int64 // bytes the segments take, headers included
	dropped uint64

	ack     *os.File
	ackSlot int // the ack file slot written next

	// unsynced counts the writes since the last sync, and wDirty, dirDirty and ackDirty what they wrote to.
	unsynced int
	wDirty   bool
	dirDirty bool
	ackDirty bool
	timer    *time.Timer // syncs WithSyncInterval after the first write not synced, while armed
	armed    bool

	err    error // a failed sync or write, after which every write fails
	closed bool

	// ready is closed while the File holds an entry, or once closed.
	ready chan struct{}
}

// FileOption configures a File.
type FileOption func(*fileConfig)

type fileConfig struct {
	maxSize      int64
	segmentSize  int64
	syncEvery    int
	syncInterval time.Duration
}

// WithMaxSize caps the bytes a File's segments take: once full, Put drops the oldest segment, so keep segments well
// below it. A record larger than the cap gets ErrTooLarge. A size of 0 or less, the default, is no cap.
func WithMaxSize(bytes int64) FileOption {
	return func(config *fileConfig) {
		config.maxSize = bytes
	}
}

// WithSegmentSize sets the bytes a segment takes before Put starts another, 1 MiB by default. A segment holds at least
// one record, and is removed once every entry in it is acked or dropped.
func WithSegmentSize(bytes int64) FileOption {
	return func(config *fileConfig) {
		config.segmentSize = bytes
	}
}

// WithSyncEvery makes a File sync once n writes (Put or Ack) are not synced, rather than after each one, since flash
// wears with each sync. Without it nor WithSyncInterval, a File syncs after each write. Close syncs what is left.
func WithSyncEvery(n int) FileOption {
	return func(config *fileConfig) {
		config.syncEvery = n
	}
}

// WithSyncInterval makes a File sync d after a write (Put or Ack) not synced, rather than after each one, since flash
// wears with each sync. Without it nor WithSyncEvery, a File syncs after each write. Close syncs what is left.
func WithSyncInterval(d time.Duration) FileOption {
	return func(config *fileConfig) {
		config.syncInterval = d
	}
}

// OpenFile opens the File in dir, creating it if needed, with every entry not acked left in it. encode and decode
// turn a message into bytes and back, and encode may be called concurrently.
func OpenFile[T any](
	dir string, encode func(T) ([]byte, error), decode func([]byte) (T, error), options ...FileOption,
) (*File[T], error) {
	config := fileConfig{segmentSize: 1 << 20}
	for _, option := range options {
		option(&config)
	}
	if config.syncEvery <= 0 && config.syncInterval <= 0 {
		config.syncEvery = 1
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	f := &File[T]{
		dir:    dir,
		encode: encode,
		decode: decode,
		config: config,
		fsync:  (*os.File).Sync,
		ready:  make(chan struct{}),
	}
	var err error
	if f.ack, err = os.OpenFile(filepath.Join(dir, ackName), os.O_RDWR|os.O_CREATE, 0o600); err != nil { //nolint:gosec // the caller gives dir
		return nil, err
	}
	if err := f.load(); err != nil {
		return nil, errors.Join(err, f.ack.Close())
	}

	return f, nil
}

// Put appends record, dropping the oldest segments if it does not fit in WithMaxSize.
func (f *File[T]) Put(_ context.Context, record subscriber.Record[T]) error {
	message, err := f.encode(record.Message)
	if err != nil {
		return err
	}
	frame := appendFrame(nil, record.SubscriberID, record.Err, message)
	if uint64(len(frame)) > math.MaxUint32 {
		return ErrTooLarge
	}
	need := int64(len(frame))

	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.writable(); err != nil {
		return err
	}
	if f.config.maxSize > 0 && segmentHeaderSize+need > f.config.maxSize {
		return ErrTooLarge
	}
	if err := f.makeRoom(need); err != nil {
		return err
	}
	if err := f.append(frame); err != nil {
		return err
	}

	return f.wrote()
}

// Next returns the oldest entry not acked, waiting for one, ctx.Err() once ctx is done, or os.ErrClosed once closed.
func (f *File[T]) Next(ctx context.Context) (Entry[T], error) {
	for {
		if err := ctx.Err(); err != nil {
			return Entry[T]{}, err
		}
		pos, payload, ready, err := f.oldest()
		if err != nil {
			return Entry[T]{}, err
		}
		if ready == nil {
			return f.entry(pos, payload)
		}
		select {
		case <-ready:
		case <-ctx.Done():
		}
	}
}

// Ack removes the entry with the given ID if it is the oldest, which is the only one Next returns, and ignores any
// other ID.
func (f *File[T]) Ack(_ context.Context, id string) error {
	pos, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil // an ID the File never gave
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.writable(); err != nil {
		return err
	}
	if f.n == 0 || pos != f.head {
		return nil
	}
	end := f.headEnd
	if end <= f.head {
		if end, err = f.frameEnd(); err != nil {
			return err
		}
	}
	removeErr := f.advance(end)
	if _, err := f.ack.WriteAt(appendAck(nil, f.head), int64(f.ackSlot*ackSlotSize)); err != nil {
		return err
	}
	f.ackSlot = 1 - f.ackSlot
	f.ackDirty = true

	return errors.Join(removeErr, f.wrote())
}

// Replay yields, when ranged, each entry the File holds then, oldest first, and acks it once yield returns: for
// subscriber.WithReplay, which hands a subscriber its dead letters from before a restart. One lost again goes back as a
// new entry, so Replay does not yield it again now. It stops early once ctx is done or the File closed, and at an entry
// it cannot read back, which stays for Next to return.
func (f *File[T]) Replay(ctx context.Context) iter.Seq[T] {
	return func(yield func(T) bool) {
		f.mu.Lock()
		end := f.tail
		f.mu.Unlock()

		for ctx.Err() == nil {
			pos, payload, ready, err := f.oldest()
			if err != nil || ready != nil || pos >= end {
				return
			}
			entry, err := f.entry(pos, payload)
			if err != nil || !yield(entry.Message) {
				return
			}
			if err := f.Ack(ctx, entry.ID); err != nil {
				return
			}
		}
	}
}

// Len returns how many entries the File holds.
func (f *File[T]) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.n
}

// Dropped returns how many entries Put dropped for lack of room since OpenFile.
func (f *File[T]) Dropped() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.dropped
}

// Close syncs what is not synced yet and closes the File's files. A Next waiting, and every call after, returns
// os.ErrClosed.
func (f *File[T]) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return os.ErrClosed
	}
	f.closed = true
	if f.n == 0 {
		close(f.ready)
	}
	if f.timer != nil {
		f.timer.Stop()
	}
	err := f.err
	if err == nil {
		err = f.sync()
	}
	for _, file := range []*os.File{f.w, f.r, f.ack} {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
	}

	return err
}

// load reads the ack file and every segment, removing those whose entries are all acked, and those with no valid
// header.
func (f *File[T]) load() error {
	var ack [2 * ackSlotSize]byte
	n, err := f.ack.ReadAt(ack[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	from, slot := parseAck(ack[:n])
	f.ackSlot = slot

	dirEntries, err := os.ReadDir(f.dir)
	if err != nil {
		return err
	}
	var bases []int64
	for _, entry := range dirEntries {
		if base, ok := parseSegmentName(entry.Name()); ok && entry.Type().IsRegular() {
			bases = append(bases, base)
		}
	}
	slices.Sort(bases)

	// A Put after a power cut may go where a torn record or an acked one was: positions only grow, so that the ack
	// file never points inside a record written after it.
	f.tail = from
	for _, base := range bases {
		path := filepath.Join(f.dir, segmentName(base))
		s, head, ok, err := scanSegment(path, base, from)
		if err != nil {
			return err
		}
		f.tail = max(f.tail, s.end())
		if !ok || s.n == 0 {
			if err := os.Remove(path); err != nil {
				return err
			}

			continue
		}
		if len(f.segments) == 0 {
			f.head = head
		}
		f.segments = append(f.segments, s)
		f.n += s.n
		f.size += segmentHeaderSize + s.size
	}
	if f.n == 0 {
		f.head = f.tail
	} else {
		close(f.ready)
	}

	return nil
}

func (f *File[T]) writable() error {
	if f.closed {
		return os.ErrClosed
	}

	return f.err
}

// makeRoom starts a new segment unless the newest has room for need bytes more, then drops the oldest segments until
// need fits in WithMaxSize, which Put checked it can.
func (f *File[T]) makeRoom(need int64) error {
	if f.w == nil || f.active().size > 0 && f.active().size+need > f.config.segmentSize {
		if err := f.roll(); err != nil {
			return err
		}
	}
	var err error
	for f.config.maxSize > 0 && f.size+need > f.config.maxSize {
		if len(f.segments) == 1 {
			// Only the newest segment is left: start another, so as to drop it.
			if err := f.roll(); err != nil {
				return err
			}
		}
		err = errors.Join(err, f.drop())
	}

	return err
}

func (f *File[T]) active() *segment {
	return &f.segments[len(f.segments)-1]
}

// roll syncs and closes the segment Put appended to, if any, and starts a new one at tail.
func (f *File[T]) roll() error {
	if f.w != nil {
		if err := f.sync(); err != nil {
			return err
		}
		if err := f.w.Close(); err != nil {
			return err
		}
		f.w = nil
	}
	path := filepath.Join(f.dir, segmentName(f.tail))
	w, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the caller gives dir
	if err != nil {
		return err
	}
	if _, err := w.WriteAt(segmentHeader[:], 0); err != nil {
		return errors.Join(err, w.Close(), os.Remove(path))
	}
	f.w = w
	f.segments = append(f.segments, segment{base: f.tail})
	f.size += segmentHeaderSize
	f.dirDirty = true
	// The segment Put appended to may hold no entry any more, and only the newest may hold none.
	if f.segments[0].n == 0 && len(f.segments) > 1 {
		return f.removeOldest()
	}

	return nil
}

// append writes frame at the end of the newest segment. A failed write is truncated away, or else every write fails
// from then on, since a record after a torn one would never be read back.
func (f *File[T]) append(frame []byte) error {
	s := f.active()
	offset := segmentHeaderSize + s.size
	if _, err := f.w.WriteAt(frame, offset); err != nil {
		if truncErr := f.w.Truncate(offset); truncErr != nil {
			f.err = errors.Join(err, truncErr)
		}

		return err
	}
	size := int64(len(frame))
	s.size += size
	s.n++
	f.size += size
	f.tail += size
	f.n++
	if f.n == 1 {
		close(f.ready)
	}
	f.wDirty = true

	return nil
}

// drop removes the oldest segment, which is not the newest, and counts its entries as dropped.
func (f *File[T]) drop() error {
	n := f.segments[0].n
	f.dropped += uint64(n) //nolint:gosec // never negative
	f.n -= n
	if f.n == 0 {
		f.ready = make(chan struct{})
	}

	return f.removeOldest()
}

// advance acks the entry at head, which ends at end, and removes the oldest segment once it holds none.
func (f *File[T]) advance(end int64) error {
	f.head, f.headEnd = end, 0
	f.n--
	f.segments[0].n--
	if f.n == 0 {
		f.ready = make(chan struct{})
	}
	if f.segments[0].n > 0 || f.w != nil && len(f.segments) == 1 {
		return nil
	}

	return f.removeOldest()
}

// removeOldest removes the oldest segment, which is not the newest, and moves head to the next one.
func (f *File[T]) removeOldest() error {
	var err error
	if f.r != nil && f.rBase == f.segments[0].base {
		err = f.r.Close()
		f.r = nil
	}
	f.size -= segmentHeaderSize + f.segments[0].size
	err = errors.Join(err, os.Remove(filepath.Join(f.dir, segmentName(f.segments[0].base))))
	f.segments = slices.Delete(f.segments, 0, 1)
	f.headEnd = 0
	if f.n == 0 {
		f.head = f.tail
	} else {
		f.head = f.segments[0].base
	}

	return err
}

// wrote counts a write, and syncs if WithSyncEvery says so, or else arms the timer of WithSyncInterval.
func (f *File[T]) wrote() error {
	f.unsynced++
	if f.config.syncEvery > 0 && f.unsynced >= f.config.syncEvery {
		return f.sync()
	}
	if f.config.syncInterval > 0 && !f.armed {
		if f.timer == nil {
			f.timer = time.AfterFunc(f.config.syncInterval, f.syncLater)
		} else {
			f.timer.Reset(f.config.syncInterval)
		}
		f.armed = true
	}

	return nil
}

func (f *File[T]) syncLater() {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.armed && f.writable() == nil {
		_ = f.sync() // its error is sticky, for the next write
	}
}

// sync makes every write so far durable: the newest segment's, then the directory's if it has a new segment, then the
// ack file's. Its error is sticky, since a failed sync may have lost writes.
func (f *File[T]) sync() error {
	if f.armed {
		f.timer.Stop()
		f.armed = false
	}
	if f.unsynced == 0 {
		return nil
	}
	if f.wDirty {
		if f.err = f.fsync(f.w); f.err != nil {
			return f.err
		}
		f.wDirty = false
	}
	if f.dirDirty {
		if f.err = f.syncDir(); f.err != nil {
			return f.err
		}
		f.dirDirty = false
	}
	if f.ackDirty {
		if f.err = f.fsync(f.ack); f.err != nil {
			return f.err
		}
		f.ackDirty = false
	}
	f.unsynced = 0

	return nil
}

// syncDir makes the segments created or removed durable.
func (f *File[T]) syncDir() error {
	dir, err := os.Open(f.dir)
	if err != nil {
		return err
	}

	return errors.Join(f.fsync(dir), dir.Close())
}

// oldest returns the record at head, its position and payload, or else a channel closed once there is one.
func (f *File[T]) oldest() (int64, []byte, <-chan struct{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return 0, nil, nil, os.ErrClosed
	}
	if f.n == 0 {
		return 0, nil, f.ready, nil
	}
	var header [frameHeaderSize]byte
	if err := f.readAt(header[:], f.head); err != nil {
		return 0, nil, nil, err
	}
	payload := make([]byte, binary.LittleEndian.Uint32(header[:4]))
	if err := f.readAt(payload, f.head+frameHeaderSize); err != nil {
		return 0, nil, nil, err
	}
	f.headEnd = f.head + frameHeaderSize + int64(len(payload))

	return f.head, payload, nil, nil
}

// frameEnd returns where the record at head ends.
func (f *File[T]) frameEnd() (int64, error) {
	var length [4]byte
	if err := f.readAt(length[:], f.head); err != nil {
		return 0, err
	}

	return f.head + frameHeaderSize + int64(binary.LittleEndian.Uint32(length[:])), nil
}

// readAt reads len(b) bytes of the oldest segment at position pos.
func (f *File[T]) readAt(b []byte, pos int64) error {
	s := f.segments[0]
	if f.r != nil && f.rBase != s.base {
		err := f.r.Close()
		f.r = nil
		if err != nil {
			return err
		}
	}
	if f.r == nil {
		r, err := os.Open(filepath.Join(f.dir, segmentName(s.base)))
		if err != nil {
			return err
		}
		f.r, f.rBase = r, s.base
	}
	_, err := f.r.ReadAt(b, segmentHeaderSize+pos-s.base)

	return err
}

// entry parses and decodes the record at pos.
func (f *File[T]) entry(pos int64, payload []byte) (Entry[T], error) {
	id := strconv.FormatInt(pos, 10)
	frame, ok := parseFrame(payload)
	if !ok {
		return Entry[T]{}, &DecodeError{ID: id, Err: errCorrupt}
	}
	message, err := f.decode(frame.message)
	if err != nil {
		return Entry[T]{}, &DecodeError{ID: id, Err: err}
	}
	record := subscriber.Record[T]{SubscriberID: frame.subscriberID, Message: message, Err: frame.err}

	return Entry[T]{Record: record, ID: id}, nil
}
