// Package transport connects a Broadcastor to the outside, at its edges: a source reads messages from a connection and
// broadcasts them, and a sink is a subscriber that writes them to one. Carrying messages from one process to another
// is a sink in the first and a source in the second. Subscribers, their errors and their stores stay local.
//
// Each protocol has its own package, which takes a connection the caller opened and closes, and never dials: packages
// udp and http. Encoding is up to the caller, with funcs that also see where a message comes from or goes to, since a
// message usually carries its address (a topic, a peer) and subscribers pick theirs with subscriber.WithFilter.
//
// A source broadcasts to a Publisher. Its SourceOptions set the message options it passes to Broadcast, which each
// source defaults to what its protocol can afford (http.Receive waits for every subscriber, udp.Receive never waits),
// and which override the subscribers' own defaults (subscriber.WithDefaultMessageOptions), delivery mode included.
//
// A sink comes in one of two shapes:
//   - Towards one peer, it is a handle to Subscribe with, like udp.Send, and gets every message in order, from the
//     subscriber's goroutine. With store.Enqueue and store.Drain in front, Broadcast never waits for the peer.
//   - Towards each of many clients, like http.Stream, it subscribes once per client to a Subscribable, with
//     store.Enqueue into a store.Ring of its own, which it drains to the client. Put never waits, so a slow client
//     never holds a Broadcast up, whatever its options: once its ring is full, it loses its oldest messages.
package transport
