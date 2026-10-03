package logging

import (
	"io"
	"log"
	"sync"
	"testing"
)

// ut-docs#3513: a test (or anything else) that swaps the global logger while
// another goroutine is already inside L() must not race. Here the reader is
// running before the swap happens, so no goroutine-creation happens-before
// edge orders the write after the reads — only the type of defaultLogger
// itself can make this safe.
func TestDefaultLoggerSwapWhileLRunningIsRaceFree(t *testing.T) {
	L() // once.Do has run; prev is non-nil
	prev := defaultLogger.Load()
	t.Cleanup(func() { defaultLogger.Store(prev) })

	running := make(chan struct{})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(running)
		for i := 0; ; i++ {
			if l := L(); l == nil {
				t.Errorf("L() returned nil on iteration %d", i)
				return
			}
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	<-running

	quiet := &Logger{level: Fatal, log: log.New(io.Discard, "", 0)}
	for i := 0; i < 500; i++ {
		if i%2 == 0 {
			defaultLogger.Store(quiet)
		} else {
			defaultLogger.Store(prev)
		}
	}
	close(stop)
	wg.Wait()
}
