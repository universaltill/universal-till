package androidupdate

import "testing"

type stubPrefetcher struct{ calls int }

func (s *stubPrefetcher) PrefetchUpdate() error { s.calls++; return nil }

// setPrefetcher stores p the way mobile.SetUpdateBridge does.
func setPrefetcher(p Prefetcher) {
	if p == nil {
		Current.Store(nil)
		return
	}
	Current.Store(&p)
}

// ut-docs#6551: no shell registered is the default and means "Activity not
// in front / Play build / non-Android".
func TestRegisteredPrefetcher_DefaultNil(t *testing.T) {
	setPrefetcher(nil)
	if got := RegisteredPrefetcher(); got != nil {
		t.Fatalf("RegisteredPrefetcher() = %#v, want nil", got)
	}
}

func TestCurrent_SetAndClear(t *testing.T) {
	t.Cleanup(func() { setPrefetcher(nil) })
	stub := &stubPrefetcher{}
	setPrefetcher(stub)
	got := RegisteredPrefetcher()
	if got != Prefetcher(stub) {
		t.Fatalf("RegisteredPrefetcher() = %#v, want the stub", got)
	}
	if err := got.PrefetchUpdate(); err != nil || stub.calls != 1 {
		t.Fatalf("PrefetchUpdate via registry: err=%v calls=%d", err, stub.calls)
	}
	setPrefetcher(nil)
	if got := RegisteredPrefetcher(); got != nil {
		t.Fatalf("after clearing Current: %#v, want nil", got)
	}
}
