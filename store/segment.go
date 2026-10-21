package store

import (
	"bufio"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/elojah/multicast/subscriber"
)

// A File's directory holds its segments and its ack file. A segment, named after the position of its first record,
// starts with segmentHeader, then holds records, each a frame:
//
//	uint32 length | uint32 CRC-32C of the length and the payload | payload
//
// and its payload:
//
//	uvarint SubscriberID | uvarint len(error text)+1, 0 without error | error text | uvarint sentinel mask | message
//
// Positions count record bytes only, headers excluded. The ack file holds two slots, each the position of the oldest
// record not acked and its CRC-32C, written in turn.
const (
	segmentExt        = ".seg"
	segmentHeaderSize = 8
	frameHeaderSize   = 8
	minPayload        = 3 // SubscriberID, error length and mask, a byte each at least
	ackName           = "ack"
	ackSlotSize       = 12

	// maxPosition bounds the positions read back, so that a corrupted one cannot overflow.
	maxPosition = 1 << 62
)

// segmentHeader is a magic and the format's version.
var segmentHeader = [segmentHeaderSize]byte{'b', 'c', 's', 't', 1}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// errCorrupt is what Next returns, in a *DecodeError, for a record it cannot parse.
var errCorrupt = errors.New("corrupt record")

// sentinels are the errors a stored error matches, a bit of its mask each, in this order: it is part of the format, so
// only append to it.
var sentinels = [...]error{
	subscriber.ErrClosed,
	subscriber.ErrTimeout,
	subscriber.ErrDropped,
	subscriber.ErrPanic,
	subscriber.ErrStore,
	subscriber.ErrEvicted,
	subscriber.ErrExpired,
}

// storedError is an error read back from a segment: its text, and the sentinels the original matched.
type storedError struct {
	text string
	mask uint64
}

func (e *storedError) Error() string {
	return e.text
}

func (e *storedError) Is(target error) bool {
	for i, sentinel := range sentinels {
		if e.mask&(1<<i) != 0 && target == sentinel {
			return true
		}
	}

	return false
}

// segment is a segment file. Every segment but the one Put appends to holds an entry not acked.
type segment struct {
	base int64 // position of its first record
	size int64 // bytes of records, header excluded
	n    int   // records not acked
}

func (s segment) end() int64 {
	return s.base + s.size
}

// frame is a record's payload, parsed.
type frame struct {
	subscriberID uint64
	err          error
	message      []byte
}

func segmentName(base int64) string {
	s := strconv.FormatInt(base, 10)

	return strings.Repeat("0", max(20-len(s), 0)) + s + segmentExt
}

// parseSegmentName returns the base of the segment named name, or false if name is no segment's.
func parseSegmentName(name string) (int64, bool) {
	digits, ok := strings.CutSuffix(name, segmentExt)
	if !ok {
		return 0, false
	}
	base, err := strconv.ParseInt(digits, 10, 64)

	return base, err == nil && base >= 0 && base <= maxPosition && segmentName(base) == name
}

// appendFrame appends a record's frame to b.
func appendFrame(b []byte, subscriberID uint64, err error, message []byte) []byte {
	start := len(b)
	b = append(b, make([]byte, frameHeaderSize)...)
	b = binary.AppendUvarint(b, subscriberID)
	if err == nil {
		b = append(b, 0, 0)
	} else {
		text := err.Error()
		b = binary.AppendUvarint(b, uint64(len(text))+1)
		b = append(b, text...)
		b = binary.AppendUvarint(b, sentinelMask(err))
	}
	b = append(b, message...)
	binary.LittleEndian.PutUint32(b[start:], uint32(len(b)-start-frameHeaderSize)) //nolint:gosec // Put checks the size
	binary.LittleEndian.PutUint32(b[start+4:], checksum(b[start:start+4], b[start+frameHeaderSize:]))

	return b
}

func sentinelMask(err error) uint64 {
	var mask uint64
	for i, sentinel := range sentinels {
		if errors.Is(err, sentinel) {
			mask |= 1 << i
		}
	}

	return mask
}

func checksum(length, payload []byte) uint32 {
	return crc32.Update(crc32.Checksum(length, castagnoli), castagnoli, payload)
}

// parseFrame parses a record's payload, or returns false if it is malformed.
func parseFrame(payload []byte) (frame, bool) {
	var f frame
	var n int
	if f.subscriberID, n = binary.Uvarint(payload); n <= 0 {
		return frame{}, false
	}
	payload = payload[n:]
	textLen, n := binary.Uvarint(payload)
	if n <= 0 || textLen > uint64(len(payload)-n) { //nolint:gosec // a length is never negative
		return frame{}, false
	}
	payload = payload[n:]
	var text string
	if textLen > 0 {
		text, payload = string(payload[:textLen-1]), payload[textLen-1:]
	}
	mask, n := binary.Uvarint(payload)
	if n <= 0 {
		return frame{}, false
	}
	if textLen > 0 {
		f.err = &storedError{text: text, mask: mask}
	}
	f.message = payload[n:]

	return f, true
}

// scanSegment reads the segment at path, based at base, and truncates it at its first frame that is short or corrupt,
// such as one a power cut tore. n counts the records at or after from, and head is the first one's position. It returns
// false if the segment has no valid header.
func scanSegment(path string, base, from int64) (segment, int64, bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // the caller gives the directory
	if err != nil {
		return segment{}, 0, false, err
	}
	defer file.Close() //nolint:errcheck // a read-only scan, or Truncate's error is returned
	info, err := file.Stat()
	if err != nil {
		return segment{}, 0, false, err
	}
	r := bufio.NewReader(file)
	var header [segmentHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil || header != segmentHeader {
		return segment{}, 0, false, ignoreEOF(err)
	}

	s := segment{base: base}
	head := int64(-1)
	remaining := info.Size() - segmentHeaderSize
	var frameHeader [frameHeaderSize]byte
	var payload []byte
	for {
		if _, err := io.ReadFull(r, frameHeader[:]); err != nil {
			if err = ignoreEOF(err); err != nil {
				return segment{}, 0, false, err
			}

			break
		}
		length := int64(binary.LittleEndian.Uint32(frameHeader[:4]))
		if length < minPayload || length > remaining-s.size-frameHeaderSize {
			break
		}
		payload = append(payload[:0], make([]byte, length)...)
		if _, err := io.ReadFull(r, payload); err != nil {
			if err = ignoreEOF(err); err != nil {
				return segment{}, 0, false, err
			}

			break
		}
		if checksum(frameHeader[:4], payload) != binary.LittleEndian.Uint32(frameHeader[4:]) {
			break
		}
		if _, ok := parseFrame(payload); !ok {
			break
		}
		if pos := s.end(); pos >= from {
			s.n++
			if head < 0 {
				head = pos
			}
		}
		s.size += frameHeaderSize + length
	}
	if s.size < remaining {
		if err := file.Truncate(segmentHeaderSize + s.size); err != nil {
			return segment{}, 0, false, err
		}
	}

	return s, head, true, nil
}

func ignoreEOF(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}

	return err
}

// parseAck returns the position the ack file data holds, its newest valid slot's, or 0 if neither is, and which slot to
// write next: the other one.
func parseAck(data []byte) (int64, int) {
	pos, next := int64(0), 0
	for slot := range 2 {
		if len(data) < (slot+1)*ackSlotSize {
			break
		}
		b := data[slot*ackSlotSize:]
		p := int64(binary.LittleEndian.Uint64(b)) //nolint:gosec // checked below
		if crc32.Checksum(b[:8], castagnoli) == binary.LittleEndian.Uint32(b[8:]) && p >= pos && p <= maxPosition {
			pos, next = p, 1-slot
		}
	}

	return pos, next
}

// appendAck appends an ack slot holding pos.
func appendAck(b []byte, pos int64) []byte {
	b = binary.LittleEndian.AppendUint64(b, uint64(pos)) //nolint:gosec // positions are never negative

	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(b[len(b)-8:], castagnoli))
}
