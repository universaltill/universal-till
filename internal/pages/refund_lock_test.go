package pages

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// lockWaitSignals maps a lock key to a channel that receives the key each
// time a lock() call has to wait (ut-docs#6742). Set from init so it is in
// place before any test runs; tests Store/Delete their own key.
var lockWaitSignals sync.Map

func init() {
	refundSaleLocks.onWait = func(key string) {
		if c, ok := lockWaitSignals.Load(key); ok {
			select {
			case c.(chan string) <- key:
			default:
			}
		}
	}
}

func watchLockWaits(t *testing.T, key string) chan string {
	t.Helper()
	c := make(chan string, 16)
	lockWaitSignals.Store(key, c)
	t.Cleanup(func() { lockWaitSignals.Delete(key) })
	return c
}

// testWaitTimeout is a hang guard, not ordering: tests wait on channels and
// fail fast if a broken lock means the awaited event never comes.
const testWaitTimeout = 10 * time.Second

func recvWithin[T any](t *testing.T, c <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(testWaitTimeout):
		t.Fatalf("timed out after %s waiting for %s", testWaitTimeout, what)
		panic("unreachable")
	}
}

// recvEither waits for the first of two events (e.g. "B parked behind A" vs
// "B reached the provider too") and fails fast if neither happens.
func recvEither[T, U any](t *testing.T, a <-chan T, aWhat string, b <-chan U, bWhat string) {
	t.Helper()
	select {
	case <-a:
	case <-b:
	case <-time.After(testWaitTimeout):
		t.Fatalf("timed out after %s waiting for %s or %s", testWaitTimeout, aWhat, bWhat)
	}
}

func TestKeyedLocks_SerialisesSameKeyAndFreesMapEntry(t *testing.T) {
	l := &keyedLocks{m: map[string]*keyedLockEntry{}}
	waits := make(chan string, 4)
	l.onWait = func(k string) { waits <- k }

	unlock1, err := l.lock(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	go func() {
		u, _ := l.lock(context.Background(), "k")
		acquired <- u
	}()
	recvWithin(t, waits, "second caller to park") // the second caller is parked behind the first
	select {
	case <-acquired:
		t.Fatal("second lock acquired while the first is held")
	default:
	}
	unlock1()
	unlock1() // idempotent
	unlock2 := recvWithin(t, acquired, "second lock to be acquired")
	unlock2()
	unlock2()
	if n := l.size(); n != 0 {
		t.Fatalf("map entries after all unlocks = %d, want 0", n)
	}
}

func TestKeyedLocks_DifferentKeysDoNotWait(t *testing.T) {
	l := &keyedLocks{m: map[string]*keyedLockEntry{}}
	l.onWait = func(k string) { t.Errorf("lock of %q had to wait", k) }
	ua, err := l.lock(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	ub, err := l.lock(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	ua()
	ub()
	if n := l.size(); n != 0 {
		t.Fatalf("size = %d", n)
	}
}

func TestKeyedLocks_CancelledWaiterGivesUpAndDropsRef(t *testing.T) {
	l := &keyedLocks{m: map[string]*keyedLockEntry{}}
	waits := make(chan string, 1)
	l.onWait = func(k string) { waits <- k }
	holder, err := l.lock(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		u, e := l.lock(ctx, "k")
		if u != nil {
			t.Error("cancelled waiter got an unlock func")
		}
		done <- e
	}()
	recvWithin(t, waits, "waiter to park")
	cancel()
	if e := recvWithin(t, done, "cancelled waiter to return"); !errors.Is(e, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", e)
	}
	holder()
	if n := l.size(); n != 0 {
		t.Fatalf("size after holder unlock = %d, want 0", n)
	}
}
