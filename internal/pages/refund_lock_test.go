package pages

import (
	"context"
	"errors"
	"sync"
	"testing"
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
	<-waits // the second caller is parked behind the first
	select {
	case <-acquired:
		t.Fatal("second lock acquired while the first is held")
	default:
	}
	unlock1()
	unlock1() // idempotent
	unlock2 := <-acquired
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
	<-waits
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", e)
	}
	holder()
	if n := l.size(); n != 0 {
		t.Fatalf("size after holder unlock = %d, want 0", n)
	}
}
