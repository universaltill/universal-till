//go:build !windows

package procjob

import (
	"os"
	"testing"
)

// TestKillWithParentIsANoOpOffWindows: elsewhere the child is left alone and
// the call never fails, so the shell's launch path is identical there.
func TestKillWithParentIsANoOpOffWindows(t *testing.T) {
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := KillWithParent(p); err != nil {
		t.Fatalf("KillWithParent = %v; want nil", err)
	}
}
