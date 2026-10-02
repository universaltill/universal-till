package logging

import (
	"log"
	"sync"
	"testing"
)

// L() must be safe to call from many goroutines before the logger is
// initialised: the recover path reaches the global logger from every
// background goroutine (ut-docs#3410). A bare `defaultLogger == nil` check
// outside once.Do races with the write inside it.
func TestLConcurrentFirstUseIsRaceFree(t *testing.T) {
	L() // make sure prev is non-nil
	prev := defaultLogger
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	t.Cleanup(func() {
		defaultLogger = prev
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})

	once = sync.Once{}
	defaultLogger = nil

	const n = 16
	start := make(chan struct{})
	got := make([]*Logger, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got[i] = L()
		}(i)
	}
	close(start)
	wg.Wait()

	for i, l := range got {
		if l == nil {
			t.Fatalf("goroutine %d got a nil logger", i)
		}
		if l != got[0] {
			t.Fatalf("goroutine %d got a different logger instance", i)
		}
	}
}
