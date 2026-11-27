package enroll

import "testing"

// A dead-credential answer that arrives before the engine exists (the
// approval-key report at start-up) is replayed once when the hook is installed.
func TestDeadCredentialBeforeTheEngineIsReplayedOnce(t *testing.T) {
	SetDeadCredentialHook(nil)
	t.Cleanup(func() { SetDeadCredentialHook(nil) })
	reportDeadCredential("approval_key", "unauthorized", "", false)
	reportDeadCredential("approval_key", "device_revoked", "removed", false) // only the first is kept
	var got []string
	SetDeadCredentialHook(func(source, code, reason string, recover bool) { got = append(got, source+"/"+code) })
	if len(got) != 1 || got[0] != "approval_key/unauthorized" {
		t.Fatalf("replayed = %v, want exactly the first signal", got)
	}
	SetDeadCredentialHook(func(source, code, reason string, recover bool) { got = append(got, "again") })
	if len(got) != 1 {
		t.Fatalf("replayed twice: %v", got)
	}
}

// Removing the hook forgets a pending early signal (no cross-run leak).
func TestEarlyDeadCredentialIsForgottenWhenTheHookIsRemoved(t *testing.T) {
	SetDeadCredentialHook(nil)
	reportDeadCredential("approval_key", "unauthorized", "", false)
	SetDeadCredentialHook(nil)
	n := 0
	SetDeadCredentialHook(func(string, string, string, bool) { n++ })
	t.Cleanup(func() { SetDeadCredentialHook(nil) })
	if n != 0 {
		t.Fatalf("a forgotten signal was replayed %d times", n)
	}
}
