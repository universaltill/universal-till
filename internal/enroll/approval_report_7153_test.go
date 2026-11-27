package enroll

import (
	"context"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ut-docs#7153: a report kicked with no WaitGroup (Recover's heal, pair, move)
// was tracked by nothing, so it could outlive its test and write into a
// t.TempDir being removed. resetApprovalReport must stop and wait for it.
func TestResetApprovalReportStopsAnUntrackedReport_7153(t *testing.T) {
	identify(t)
	c := newReportCloud(t, http.StatusInternalServerError) // reportRetry: parks on the 15 min timer
	kickApprovalKeyReport(context.Background(), marketplace(c), nil)
	waitFor(t, "the report's first attempt", func() bool { return c.hits.Load() >= 1 })

	done := make(chan struct{})
	go func() { resetApprovalReport(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("resetApprovalReport did not stop the parked report goroutine")
	}
	// reset returned, so the goroutine must already be gone, not merely
	// unregistered: look for its frame in the live stacks.
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf)) // truncated: grow, or a live frame could be missed
	}
	if strings.Contains(string(buf), "enroll.kickApprovalKeyReport.func") {
		t.Fatal("the kicked report goroutine is still running after resetApprovalReport")
	}
}
