package enroll

import (
	"context"
	"time"
)

// Test-only views of the approval-report state. ApprovalKeyConflict becomes
// an exported accessor with its first caller (the Settings > Cloud conflict
// text, ut-docs#5127 UT-1b); until then the deadcode baseline guard forbids
// an unreachable exported func in the build.

// ApprovalKeyConflict reports whether the cloud answered 409 (a different
// approval key is pinned for this till).
func ApprovalKeyConflict() bool { return approvalConflict.Load() }

func resetApprovalReport() {
	// Stop and wait for every report goroutine first, including untracked
	// ones (ut-docs#7153). Not under approvalReport's mutex: the goroutine's
	// defer takes it.
	approvalBG.Lock()
	if approvalBG.end != nil {
		approvalBG.end()
	}
	approvalBG.Unlock()
	approvalBG.wg.Wait()
	approvalBG.Lock()
	approvalBG.stop, approvalBG.end = context.WithCancel(context.Background())
	approvalBG.Unlock()
	approvalReport.Lock()
	approvalReport.done, approvalReport.inflight = nil, nil
	approvalReport.Unlock()
	approvalConflict.Store(false)
}

// setApprovalRetryDelays swaps the retry waits for one test under the lock
// that guards them, and restores them in Cleanup. Never assign
// approvalRetryDelays directly in a test: a report goroutine from an earlier
// test may still be running (ut-docs#7009).
func setApprovalRetryDelays(t interface{ Cleanup(func()) }, d []time.Duration) {
	approvalReport.Lock()
	old := approvalRetryDelays
	approvalRetryDelays = d
	approvalReport.Unlock()
	t.Cleanup(func() {
		approvalReport.Lock()
		approvalRetryDelays = old
		approvalReport.Unlock()
	})
}

// setApprovalRetryTail swaps the wait between attempts after the listed ones.
func setApprovalRetryTail(t interface{ Cleanup(func()) }, d time.Duration) {
	approvalReport.Lock()
	old := approvalRetryTail
	approvalRetryTail = d
	approvalReport.Unlock()
	t.Cleanup(func() {
		approvalReport.Lock()
		approvalRetryTail = old
		approvalReport.Unlock()
	})
}
