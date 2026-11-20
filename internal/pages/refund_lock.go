package pages

import (
	"context"
	"sync"
)

// keyedLocks: one in-process, context-cancellable mutex per key
// (ut-docs#6742). A key's entry exists only while someone holds or waits for
// it, so the map's size is the number of refunds in flight, not the number of
// sales ever refunded. Not reentrant: a holder that locks its own key again
// deadlocks (until its context is done).
type keyedLocks struct {
	mu     sync.Mutex
	m      map[string]*keyedLockEntry
	onWait func(key string) // test seam, set only from a _test.go init(); nil in prod
}

type keyedLockEntry struct {
	ch   chan struct{} // cap 1: a send takes the lock, a receive releases it
	refs int           // holders + waiters, guarded by keyedLocks.mu
}

// refundSaleLocks serialises POST /api/refund per original sale id. Keys are
// sale UUIDs, so tills sharing the process (tests) cannot collide.
var refundSaleLocks = &keyedLocks{m: map[string]*keyedLockEntry{}}

// lock blocks until key is free or ctx is done. On success it returns an
// idempotent unlock; on ctx.Err() it returns (nil, err) having dropped its
// reference.
func (l *keyedLocks) lock(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	e := l.m[key]
	if e == nil {
		e = &keyedLockEntry{ch: make(chan struct{}, 1)}
		l.m[key] = e
	}
	e.refs++
	l.mu.Unlock()

	select {
	case e.ch <- struct{}{}:
	default:
		if l.onWait != nil {
			l.onWait(key)
		}
		select {
		case e.ch <- struct{}{}:
		case <-ctx.Done():
			l.drop(key, e)
			return nil, ctx.Err()
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-e.ch
			l.drop(key, e)
		})
	}, nil
}

// drop releases one reference and deletes the entry when none are left.
func (l *keyedLocks) drop(key string, e *keyedLockEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.refs--
	if e.refs == 0 {
		delete(l.m, key)
	}
}
