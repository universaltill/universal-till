//go:build !windows

// syscall.Mkfifo does not exist on Windows (ut-docs#2287 review, finding 5):
// release.yml cross-compiles a Windows binary, and while tests are not
// cross-compiled today, `GOOS=windows go vet ./internal/print/` must keep
// compiling the test package.

package print

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestDeviceTransport_ConcurrentWritesToSamePathDoNotInterleave uses a
// FIFO rather than a plain regular file: a regular file's O_WRONLY open
// resets the write position to 0 regardless of concurrency, which doesn't
// exercise the actual bug (two concurrent writers' bytes interleaving on
// the wire) the way a real unbuffered USB character device does. A FIFO's
// limited kernel pipe buffer forces Go's Write() into multiple underlying
// write(2) syscalls for a large payload, so an unsynchronised pair of
// writers really does interleave chunks — matching the real hazard.
func TestDeviceTransport_ConcurrentWritesToSamePathDoNotInterleave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lp0")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	payloadA := bytes.Repeat([]byte{'A'}, 64*1024)
	payloadB := bytes.Repeat([]byte{'B'}, 64*1024)
	want := len(payloadA) + len(payloadB)

	received := make([]byte, 0, want)
	readErrCh := make(chan error, 1)
	go func() {
		f, ferr := os.OpenFile(path, os.O_RDONLY, 0)
		if ferr != nil {
			readErrCh <- ferr
			return
		}
		defer f.Close()
		buf := make([]byte, 4096)
		for len(received) < want {
			n, rerr := f.Read(buf)
			if n > 0 {
				received = append(received, buf[:n]...)
			}
			if rerr != nil {
				if rerr == io.EOF {
					if len(received) >= want {
						break
					}
					// Transient EOF: no writer currently has the FIFO
					// open (the two Print calls are serialised, so
					// there's a real gap between job 1's close and job
					// 2's open). Keep the same read fd and retry — a new
					// writer opening will unblock the next Read.
					time.Sleep(time.Millisecond)
					continue
				}
				readErrCh <- rerr
				return
			}
		}
		readErrCh <- nil
	}()

	tr := &deviceTransport{path: path}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = tr.Print(context.Background(), payloadA) }()
	go func() { defer wg.Done(); errs[1] = tr.Print(context.Background(), payloadB) }()
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("unexpected error: %v / %v", errs[0], errs[1])
	}

	select {
	case rerr := <-readErrCh:
		if rerr != nil {
			t.Fatalf("reader error: %v", rerr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reader to collect both payloads")
	}

	if len(received) != want {
		t.Fatalf("expected %d combined bytes, got %d", want, len(received))
	}
	okAB := bytes.Equal(received[:len(payloadA)], payloadA) && bytes.Equal(received[len(payloadA):], payloadB)
	okBA := bytes.Equal(received[:len(payloadB)], payloadB) && bytes.Equal(received[len(payloadB):], payloadA)
	if !okAB && !okBA {
		t.Fatal("payloads interleaved or corrupted — expected one full job followed by the other")
	}
}
