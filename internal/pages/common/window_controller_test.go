package common

import "testing"

// TestNoopWindowController_RecordInputHeartbeatIsNil (ut-docs#1329): the
// bare-Deps/test fallback stays a pure no-op for the new interface method,
// same as its existing ExitToOS/ApplyMode.
func TestNoopWindowController_RecordInputHeartbeatIsNil(t *testing.T) {
	if err := (NoopWindowController{}).RecordInputHeartbeat(); err != nil {
		t.Fatalf("RecordInputHeartbeat() = %v, want nil", err)
	}
}

// TestAndroidNativeWindowController_RecordInputHeartbeatIsNil (ut-docs#1329):
// no separate unitill-desktop process exists on this platform, so this is a
// documented no-op — same shape as its ApplyMode/ExitToOS.
func TestAndroidNativeWindowController_RecordInputHeartbeatIsNil(t *testing.T) {
	if err := (AndroidNativeWindowController{}).RecordInputHeartbeat(); err != nil {
		t.Fatalf("RecordInputHeartbeat() = %v, want nil", err)
	}
}
