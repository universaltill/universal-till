package pairingpush

import (
	"testing"
	"time"
)

func TestPublishNeverBlocksOnFullUnreadSubscriber(t *testing.T) {
	b := New(4)
	_, cancel, ok := b.Subscribe()
	if !ok {
		t.Fatal("subscribe refused")
	}
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			b.Publish()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a full, unread subscriber")
	}
}

func TestPublishCoalesces(t *testing.T) {
	b := New(4)
	ch, cancel, _ := b.Subscribe()
	defer cancel()
	b.Publish()
	b.Publish()
	b.Publish()
	<-ch
	select {
	case <-ch:
		t.Fatal("expected exactly one pending nudge")
	default:
	}
}

func TestCapAndCancelFreesSlot(t *testing.T) {
	b := New(1)
	_, cancel, ok := b.Subscribe()
	if !ok {
		t.Fatal("first subscribe refused")
	}
	if _, _, ok2 := b.Subscribe(); ok2 {
		t.Fatal("second subscribe past the cap must be refused")
	}
	cancel()
	cancel() // idempotent
	_, cancel3, ok3 := b.Subscribe()
	if !ok3 {
		t.Fatal("slot not freed by cancel")
	}
	cancel3()
}

func TestCloseClosesChannelsAndIsInert(t *testing.T) {
	b := New(2)
	ch, cancel, _ := b.Subscribe()
	b.Close()
	b.Close()
	if _, open := <-ch; open {
		t.Fatal("channel should be closed after Close")
	}
	cancel() // must not panic
	b.Publish()
	ch2, cancel2, ok := b.Subscribe()
	if !ok {
		t.Fatal("subscribe after Close should return a closed channel, ok=true")
	}
	defer cancel2()
	if _, open := <-ch2; open {
		t.Fatal("channel after Close should be closed")
	}
}
