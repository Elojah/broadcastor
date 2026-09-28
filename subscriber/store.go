package subscriber

import (
	"context"

	"github.com/google/uuid"
)

// Store is given every message a subscriber loses, when passed to WithStore, so that it can be handled again later:
// every message handle or its middlewares fail on, every message Broadcast could not hand the subscriber, and every
// message the subscriber took but discarded. Put is called right before the error handlers, from wherever they are
// called, so it may be called from several goroutines at once, and a slow Put holds things up like a slow error
// handler.
type Store[T any] interface {
	// Put stores r, or returns why it could not. ctx has the values of the ctx the message is handled with, the one
	// message.WithContext gave it or else the subscriber's, but is never done: that ctx being done is often why the
	// message was lost, so Put must bound itself.
	Put(ctx context.Context, r Record[T]) error
}

// Record is what a Store is given about a message a subscriber lost.
type Record[T any] struct {
	// SubscriberID is the ID of the subscriber that lost the message.
	SubscriberID uuid.UUID
	// Message is the message it lost.
	Message T
	// Err is why it lost it: the error its error handlers are given, unless Put fails.
	Err error
}
