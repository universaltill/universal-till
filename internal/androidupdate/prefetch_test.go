package androidupdate

import "testing"

type stubPrefetcher struct{ calls int }

func (s *stubPrefetcher) PrefetchUpdate() error { s.calls++; return nil }

// ut-docs#6551: no shell registered is the default and means "Activity not
// in front / Play build / non-Android".
func TestRegisteredPrefetcher_DefaultNil(t *testing.T) {
	SetPrefetcher(nil)
	if got := RegisteredPrefetcher(); got != nil {
		t.Fatalf("RegisteredPrefetcher() = %#v, want nil", got)
	}
}

func TestSetPrefetcher_SetAndClear(t *testing.T) {
	t.Cleanup(func() { SetPrefetcher(nil) })
	stub := &stubPrefetcher{}
	SetPrefetcher(stub)
	got := RegisteredPrefetcher()
	if got != Prefetcher(stub) {
		t.Fatalf("RegisteredPrefetcher() = %#v, want the stub", got)
	}
	if err := got.PrefetchUpdate(); err != nil || stub.calls != 1 {
		t.Fatalf("PrefetchUpdate via registry: err=%v calls=%d", err, stub.calls)
	}
	SetPrefetcher(nil)
	if got := RegisteredPrefetcher(); got != nil {
		t.Fatalf("after SetPrefetcher(nil): %#v, want nil", got)
	}
}
