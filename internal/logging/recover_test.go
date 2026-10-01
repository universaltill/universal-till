package logging

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"
)

// ut-docs#3304: a goroutine that panics under `defer RecoverAndLog(name)`
// ends quietly instead of taking the till down, and the panic lands in the
// log — named, with a stack, and redacted.
func TestRecoverAndLogRecoversAndLogsRedacted(t *testing.T) {
	const secret = "s3cr3t-Merchant-T0ken"

	var buf syncBuffer
	prev := defaultLogger
	L() // make sure once.Do has run, so it never overwrites our swap
	defaultLogger = &Logger{level: Info, log: log.New(redactingWriter{w: &buf}, "", 0)}
	t.Cleanup(func() { defaultLogger = prev })
	ResetRecent()
	t.Cleanup(ResetRecent)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer RecoverAndLog("test.worker")
		panic("merchant_token=" + secret)
	}()
	wg.Wait() // reached only because the panic was recovered

	out := buf.String()
	if !strings.Contains(out, "[ERROR] [test.worker] recovered from goroutine panic: merchant_token="+redactedMark) {
		t.Fatalf("panic not logged as a named, redacted ERROR: %q", out)
	}
	if !strings.Contains(out, "goroutine ") || !strings.Contains(out, "recover_test.go") {
		t.Errorf("no stack trace in the log line: %q", out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("raw secret reached the log: %q", out)
	}
	rec := Recent()
	if len(rec) == 0 || !strings.Contains(rec[0].Msg, "[test.worker]") || strings.Contains(rec[0].Msg, secret) {
		t.Errorf("Problems ring missing the redacted panic: %+v", rec)
	}
}

// A goroutine that returns normally logs nothing.
func TestRecoverAndLogNoPanicIsSilent(t *testing.T) {
	var buf syncBuffer
	prev := defaultLogger
	L()
	defaultLogger = &Logger{level: Debug, log: log.New(&buf, "", 0)}
	t.Cleanup(func() { defaultLogger = prev })

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer RecoverAndLog("test.quiet")
	}()
	<-done
	if buf.Len() != 0 {
		t.Fatalf("logged without a panic: %q", buf.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Len()
}
