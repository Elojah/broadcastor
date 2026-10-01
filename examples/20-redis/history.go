package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/elojah/broadcastor/subscriber"
)

// history is a subscriber.History kept in a Redis sorted set, scored by the time each message was appended, so that it
// outlives the process. It keeps the messages appended within maxAge.
//
// Offsets count from 1 again with each Broadcastor, so a history marks the entries it appends with a run ID of its own,
// and Read returns those of earlier runs at offset 0. Give each Broadcastor its own history, and share a key only with
// the histories of those that ran before it.
type history[T any] struct {
	client *redis.Client
	key    string
	maxAge time.Duration
	run    string
}

// historyEntry is a member of the sorted set. Its run and offset make it unique, so that a message broadcast twice is
// kept twice.
type historyEntry[T any] struct {
	Run     string `json:"run"`
	Offset  uint64 `json:"offset"`
	Message T      `json:"message"`
}

func newHistory[T any](client *redis.Client, key string, maxAge time.Duration) *history[T] {
	return &history[T]{client: client, key: key, maxAge: maxAge, run: uuid.NewString()}
}

// Append adds msg and drops the messages older than maxAge, in one transaction. A failure is only logged: the message
// is broadcast all the same, but never replayed.
func (h *history[T]) Append(ctx context.Context, offset uint64, msg T) {
	member, err := json.Marshal(historyEntry[T]{Run: h.run, Offset: offset, Message: msg})
	if err != nil {
		log.Printf("history: encoding message %d: %v", offset, err)

		return
	}
	now := time.Now()
	if _, err := h.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZAdd(ctx, h.key, redis.Z{Score: float64(now.UnixMilli()), Member: member})
		pipe.ZRemRangeByScore(ctx, h.key, "-inf", "("+h.oldest(now))

		return nil
	}); err != nil {
		log.Printf("history: appending message %d: %v", offset, err)
	}
}

// Read returns the messages appended within maxAge, oldest first: those of earlier runs at offset 0, and those of this
// one at the offset Append was given, which the library sorts them by.
func (h *history[T]) Read(ctx context.Context) ([]subscriber.HistoryEntry[T], error) {
	members, err := h.client.ZRangeByScoreWithScores(ctx, h.key, &redis.ZRangeBy{Min: h.oldest(time.Now()), Max: "+inf"}).Result()
	if err != nil {
		return nil, err
	}
	type scored struct {
		score float64
		entry historyEntry[T]
	}
	read := make([]scored, len(members))
	for i, z := range members {
		member, _ := z.Member.(string) // go-redis reads every member as a string
		if err := json.Unmarshal([]byte(member), &read[i].entry); err != nil {
			return nil, fmt.Errorf("decoding %q: %w", member, err)
		}
		read[i].score = z.Score
	}
	// Redis orders messages appended within the same millisecond by their JSON, where offset 10 comes before 9.
	slices.SortFunc(read, func(a, b scored) int {
		return cmp.Or(cmp.Compare(a.score, b.score), cmp.Compare(a.entry.Offset, b.entry.Offset))
	})

	entries := make([]subscriber.HistoryEntry[T], len(read))
	for i, r := range read {
		entries[i].Message = r.entry.Message
		if r.entry.Run == h.run {
			entries[i].Offset = r.entry.Offset
		}
	}

	return entries, nil
}

// oldest returns the score of the oldest message kept at now.
func (h *history[T]) oldest(now time.Time) string {
	return strconv.FormatInt(now.Add(-h.maxAge).UnixMilli(), 10)
}
