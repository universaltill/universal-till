package enroll

// Test-only views of the approval-report state. ApprovalKeyConflict becomes
// an exported accessor with its first caller (the Settings > Cloud conflict
// text, ut-docs#5127 UT-1b); until then the deadcode baseline guard forbids
// an unreachable exported func in the build.

// ApprovalKeyConflict reports whether the cloud answered 409 (a different
// approval key is pinned for this till).
func ApprovalKeyConflict() bool { return approvalConflict.Load() }

func resetApprovalReport() {
	approvalReport.Lock()
	approvalReport.done, approvalReport.inflight = nil, nil
	approvalReport.Unlock()
	approvalConflict.Store(false)
}
