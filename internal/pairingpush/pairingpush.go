// Package pairingpush is the in-process "pending pairings changed, re-read"
// nudge behind the manager's live pairing queue (ut-docs#2743, ADR-0114 §2,
// ADR-0079 §1 pattern). It carries no payload: a subscriber learns only that
// something changed and re-fetches the existing fragments, so no device name,
// code or id ever travels on the stream. It is not a delivery-guaranteed
// queue — nudges coalesce, and the 30s polls remain the fallback.
package pairingpush

import "sync"

// Broadcaster fans a payload-free nudge out to a capped set of subscribers.
type Broadcaster struct {
	mu     sync.Mutex
	next   int
	max    int
	subs   map[int]chan struct{}
	closed bool
}

// New returns a Broadcaster that admits at most maxSubs concurrent subscribers.
func New(maxSubs int) *Broadcaster {
	return &Broadcaster{max: maxSubs, subs: map[int]chan struct{}{}}
}

// Subscribe registers one subscriber with a one-slot buffer. ok is false (and
// nothing is registered) when the cap is reached. After Close it returns an
// already-closed channel and a no-op cancel. cancel is idempotent.
func (b *Broadcaster) Subscribe() (ch <-chan struct{}, cancel func(), ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		c := make(chan struct{})
		close(c)
		return c, func() {}, true
	}
	if len(b.subs) >= b.max {
		return nil, func() {}, false
	}
	id := b.next
	b.next++
	c := make(chan struct{}, 1)
	b.subs[id] = c
	return c, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if sc, present := b.subs[id]; present {
			delete(b.subs, id)
			close(sc)
		}
	}, true
}

// Publish nudges every subscriber without ever blocking: a subscriber whose
// slot is already full already has a nudge pending, so this one coalesces.
func (b *Broadcaster) Publish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.subs {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// Close closes every subscriber channel and makes the Broadcaster inert.
// Idempotent.
func (b *Broadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, c := range b.subs {
		delete(b.subs, id)
		close(c)
	}
}
