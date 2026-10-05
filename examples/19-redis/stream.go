package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/elojah/broadcastor/store"
	"github.com/elojah/broadcastor/subscriber"
)

// nextWait is how long Next blocks in Redis before it checks ctx again: go-redis does not end a blocking read when ctx
// is done.
const nextWait = time.Second

var _ store.Queue[int] = (*stream[int])(nil)

// stream is a store.Queue kept in a Redis stream, where it outlives the process: Put adds each record, along with the
// subscriber's ID and the error's text, and store.Drain hands them back, oldest first. The stream keeps about the last
// maxLen of them.
type stream[T any] struct {
	client *redis.Client
	key    string
	maxLen int64
}

func newStream[T any](client *redis.Client, key string, maxLen int64) *stream[T] {
	return &stream[T]{client: client, key: key, maxLen: maxLen}
}

// Put adds r to the stream, with the text of its error if it has one: middleware.History and store.Enqueue put none.
// Its ctx is never done, so the client's read and write timeouts bound it.
func (s *stream[T]) Put(ctx context.Context, r subscriber.Record[T]) error {
	msg, err := json.Marshal(r.Message)
	if err != nil {
		return err
	}
	values := []any{"subscriber", r.SubscriberID.String(), "message", msg}
	if r.Err != nil {
		values = append(values, "error", r.Err.Error())
	}

	return s.client.XAdd(ctx, &redis.XAddArgs{Stream: s.key, MaxLen: s.maxLen, Approx: true, Values: values}).Err()
}

// Next returns the oldest entry not yet acked, waiting for one, or ctx.Err() once ctx is done, within nextWait.
func (s *stream[T]) Next(ctx context.Context) (store.Entry[T], error) {
	for {
		if err := ctx.Err(); err != nil {
			return store.Entry[T]{}, err
		}
		// After ID 0 comes the oldest entry, since Ack deletes those handled.
		streams, err := s.client.XRead(ctx, &redis.XReadArgs{Streams: []string{s.key, "0"}, Count: 1, Block: nextWait}).Result()
		switch {
		case errors.Is(err, redis.Nil): // none within nextWait
		case err != nil:
			return store.Entry[T]{}, err
		default:
			return s.decode(streams[0].Messages[0])
		}
	}
}

// Ack deletes the entry with the given ID, if the stream still holds it.
func (s *stream[T]) Ack(ctx context.Context, id string) error {
	return s.client.XDel(ctx, s.key, id).Err()
}

// Len returns how many entries the stream holds.
func (s *stream[T]) Len(ctx context.Context) (int64, error) {
	return s.client.XLen(ctx, s.key).Result()
}

// All returns every entry the stream holds, oldest first, without acking any.
func (s *stream[T]) All(ctx context.Context) ([]store.Entry[T], error) {
	messages, err := s.client.XRange(ctx, s.key, "-", "+").Result()
	if err != nil {
		return nil, err
	}
	entries := make([]store.Entry[T], 0, len(messages))
	for _, x := range messages {
		entry, err := s.decode(x)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// decode reads back an entry Put added. go-redis reads every value as a string.
func (s *stream[T]) decode(x redis.XMessage) (store.Entry[T], error) {
	entry := store.Entry[T]{ID: x.ID}
	id, _ := x.Values["subscriber"].(string)
	msg, _ := x.Values["message"].(string)
	var err error
	if entry.SubscriberID, err = uuid.Parse(id); err != nil {
		return store.Entry[T]{}, fmt.Errorf("entry %s: %w", x.ID, err)
	}
	if err := json.Unmarshal([]byte(msg), &entry.Message); err != nil {
		return store.Entry[T]{}, fmt.Errorf("entry %s: %w", x.ID, err)
	}
	if text, ok := x.Values["error"].(string); ok {
		entry.Err = storedError(text)
	}

	return entry, nil
}

// storedError is an error read back from the stream, which keeps only its text.
type storedError string

func (e storedError) Error() string {
	return string(e)
}
