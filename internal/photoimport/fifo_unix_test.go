//go:build unix

package photoimport

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadInFolder_FIFOAndDeviceNamesDoNotBlock(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "p.png")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo unavailable")
	}
	done := make(chan error, 1)
	go func() { _, err := ReadInFolder(root, "p.png", 100); done <- err }()
	select {
	case err := <-done:
		if err != ErrNotRegular {
			t.Fatalf("FIFO = %v, want ErrNotRegular", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("FIFO read blocked")
	}
}
