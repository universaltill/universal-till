package common

import (
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/kiosk"
)

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

// ut-docs#3466 (ADR-0142 D3): ReleaseKiosk only does something on Android,
// the one platform where self-order pins the OS. Every other controller
// refuses with the sentinel the kiosk_unlock handler maps to its result
// reason — never a panic, never a fabricated success.
func TestReleaseKiosk_NonAndroidControllersRefuse(t *testing.T) {
	for name, tc := range map[string]struct {
		wc   WindowController
		want error
	}{
		"noop":                       {NoopWindowController{}, ErrKioskReleaseNotSupported},
		"pi kiosk":                   {KioskSystemdWindowController{run: func(string) error { t.Fatal("ReleaseKiosk must not run systemctl"); return nil }}, ErrNoOSDesktop},
		"shell poll":                 {NewShellPollWindowController(NewShellChannel("normal"), nil), ErrKioskReleaseNotSupported},
		"shell poll + http fallback": {NewShellPollWindowController(NewShellChannel("normal"), HTTPWindowController{addr: "127.0.0.1:1"}), ErrKioskReleaseNotSupported},
		"http":                       {HTTPWindowController{addr: "127.0.0.1:1"}, ErrKioskReleaseNotSupported},
	} {
		t.Run(name, func(t *testing.T) {
			if err := tc.wc.ReleaseKiosk(); !errors.Is(err, tc.want) {
				t.Fatalf("ReleaseKiosk() = %v, want %v", err, tc.want)
			}
		})
	}
}

type fakeKioskBridge struct {
	calls int
	err   error
}

func (f *fakeKioskBridge) ReleaseKiosk() error { f.calls++; return f.err }

// ADR-0142 D3: with no bridge registered (MainActivity not in the
// foreground) the answer is no_shell; with one, the call reaches it and
// its error comes back as-is.
func TestAndroidNativeWindowController_ReleaseKiosk(t *testing.T) {
	t.Cleanup(func() { kiosk.SetBridge(nil) })
	wc := AndroidNativeWindowController{}

	kiosk.SetBridge(nil)
	if err := wc.ReleaseKiosk(); !errors.Is(err, ErrNoKioskShell) {
		t.Fatalf("no bridge: %v, want ErrNoKioskShell", err)
	}

	b := &fakeKioskBridge{}
	kiosk.SetBridge(b)
	if err := wc.ReleaseKiosk(); err != nil || b.calls != 1 {
		t.Fatalf("bridge: err=%v calls=%d", err, b.calls)
	}

	boom := errors.New("stopLockTask refused")
	kiosk.SetBridge(&fakeKioskBridge{err: boom})
	if err := wc.ReleaseKiosk(); !errors.Is(err, boom) {
		t.Fatalf("bridge error: %v, want %v", err, boom)
	}
}

// The reason texts are a contract with ut-cloud/my. (ADR-0142 D3).
func TestReleaseKioskSentinelTexts(t *testing.T) {
	if ErrKioskReleaseNotSupported.Error() != "not_supported" || ErrNoKioskShell.Error() != "no_shell" {
		t.Fatalf("sentinels: %q %q", ErrKioskReleaseNotSupported, ErrNoKioskShell)
	}
}
