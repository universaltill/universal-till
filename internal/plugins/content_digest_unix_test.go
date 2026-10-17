//go:build unix

package plugins

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A FIFO (or any other non-regular, non-directory entry) is refused.
func TestComputeContentDigest_RefusesNonRegularFile(t *testing.T) {
	dir := writeTree(t, map[string][]byte{"a.txt": []byte("hello\n")})
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	if got, err := ComputeContentDigest(dir); err == nil {
		t.Fatalf("fifo accepted, digest %s", got)
	} else if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error should say not a regular file: %v", err)
	}
}
