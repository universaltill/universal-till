package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/kiosk"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3466 (ADR-0142 D3): the kiosk_unlock hook. The till's own
// display.mode is the security boundary — outside self-order it refuses
// without touching the window controller. Inside, it calls ReleaseKiosk
// and maps each platform's refusal onto the result reason my. shows.
// Every outcome, success or failure, leaves a till audit row.

// kioskSpy is a WindowController that records ReleaseKiosk calls.
type kioskSpy struct {
	calls int
	err   error
}

func (s *kioskSpy) ExitToOS() error             { return nil }
func (s *kioskSpy) ApplyMode(string) error      { return nil }
func (s *kioskSpy) RecordInputHeartbeat() error { return nil }
func (s *kioskSpy) ReleaseKiosk() error         { s.calls++; return s.err }

func kioskUnlockAudits(t *testing.T, dp *common.Deps) []map[string]any {
	t.Helper()
	rows, err := dp.Db.QueryContext(t.Context(),
		`SELECT actor_id, entity_type, entity_id, data_json FROM audit_log WHERE action = 'kiosk_unlock' ORDER BY created_at`)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var actor, typ, id, js string
		if err := rows.Scan(&actor, &typ, &id, &js); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		row := map[string]any{}
		if err := json.Unmarshal([]byte(js), &row); err != nil {
			t.Fatalf("audit payload %q: %v", js, err)
		}
		row["_actor"], row["_entity_type"], row["_entity_id"] = actor, typ, id
		out = append(out, row)
	}
	return out
}

func setDisplayMode(t *testing.T, dp *common.Deps, mode string) {
	t.Helper()
	if err := dp.Settings.Set(t.Context(), "display.mode", mode); err != nil {
		t.Fatal(err)
	}
}

func TestCloudKioskUnlock_RefusedOutsideSelfOrder(t *testing.T) {
	for _, mode := range []string{"", "till", "backoffice", "kiosk", "SELF_ORDER"} {
		t.Run("mode="+mode, func(t *testing.T) {
			dp := newCloudSyncTestDeps(t)
			spy := &kioskSpy{}
			dp.WindowCtl = spy
			if mode != "" {
				setDisplayMode(t, dp, mode)
			}
			msg, err := cloudKioskUnlock(t.Context(), dp, "dir-1", "owner-sub")
			if err == nil || err.Error() != "not_self_order" {
				t.Fatalf("got (%q, %v), want error not_self_order", msg, err)
			}
			if spy.calls != 0 {
				t.Fatalf("ReleaseKiosk called %d times outside self-order mode", spy.calls)
			}
			a := kioskUnlockAudits(t, dp)
			if len(a) != 1 || a[0]["status"] != "failed" || a[0]["reason"] != "not_self_order" ||
				a[0]["directive_id"] != "dir-1" || a[0]["created_by"] != "owner-sub" || a[0]["_actor"] != "system" {
				t.Fatalf("audit = %+v", a)
			}
		})
	}
}

func TestCloudKioskUnlock_ReleasesInSelfOrder(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	spy := &kioskSpy{}
	dp.WindowCtl = spy
	setDisplayMode(t, dp, "self_order")

	msg, err := cloudKioskUnlock(t.Context(), dp, "dir-1", "owner-sub")
	if err != nil || msg != "kiosk released" {
		t.Fatalf("got (%q, %v)", msg, err)
	}
	if spy.calls != 1 {
		t.Fatalf("ReleaseKiosk calls = %d, want 1", spy.calls)
	}
	a := kioskUnlockAudits(t, dp)
	if len(a) != 1 {
		t.Fatalf("audit rows = %+v, want 1", a)
	}
	r := a[0]
	if r["_actor"] != "system" || r["_entity_type"] != "kiosk" || r["_entity_id"] != "dir-1" ||
		r["directive_id"] != "dir-1" || r["created_by"] != "owner-sub" || r["status"] != "applied" || r["via"] != "cloud" {
		t.Fatalf("audit row = %+v", r)
	}
	if _, has := r["reason"]; has {
		t.Fatalf("a success carries no reason: %+v", r)
	}
}

func TestCloudKioskUnlock_PlatformRefusalsMapToReasons(t *testing.T) {
	for name, tc := range map[string]struct {
		wc   common.WindowController
		want string
	}{
		"pi appliance":         {&kioskSpy{err: common.ErrNoOSDesktop}, "kiosk_appliance"},
		"pi appliance, real":   {common.KioskSystemdWindowController{}, "kiosk_appliance"},
		"desktop shell":        {&kioskSpy{err: common.ErrKioskReleaseNotSupported}, "not_supported"},
		"noop":                 {common.NoopWindowController{}, "not_supported"},
		"no window controller": {nil, "not_supported"},
		"android, no activity": {&kioskSpy{err: common.ErrNoKioskShell}, "no_shell"},
		"wrapped sentinel":     {&kioskSpy{err: fmt.Errorf("shell: %w", common.ErrNoOSDesktop)}, "kiosk_appliance"},
		"bridge failure":       {&kioskSpy{err: errors.New("stopLockTask refused")}, "stopLockTask refused"},
	} {
		t.Run(name, func(t *testing.T) {
			dp := newCloudSyncTestDeps(t)
			dp.WindowCtl = tc.wc
			setDisplayMode(t, dp, "self_order")
			_, err := cloudKioskUnlock(t.Context(), dp, "dir-9", "admin-sub")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			a := kioskUnlockAudits(t, dp)
			if len(a) != 1 || a[0]["status"] != "failed" || a[0]["reason"] != tc.want ||
				a[0]["directive_id"] != "dir-9" || a[0]["created_by"] != "admin-sub" {
				t.Fatalf("audit = %+v", a)
			}
		})
	}
}

// The real Android path end to end on the Go side: hook → controller →
// registered bridge.
func TestCloudKioskUnlock_AndroidControllerReachesBridge(t *testing.T) {
	t.Cleanup(func() { kiosk.SetBridge(nil) })
	dp := newCloudSyncTestDeps(t)
	dp.WindowCtl = common.AndroidNativeWindowController{}
	setDisplayMode(t, dp, "self_order")

	kiosk.SetBridge(nil)
	if _, err := cloudKioskUnlock(t.Context(), dp, "d1", "s"); err == nil || err.Error() != "no_shell" {
		t.Fatalf("no bridge: %v", err)
	}
	b := &kioskSpy{}
	kiosk.SetBridge(b)
	if msg, err := cloudKioskUnlock(t.Context(), dp, "d2", "s"); err != nil || msg != "kiosk released" || b.calls != 1 {
		t.Fatalf("bridge: (%q, %v), calls=%d", msg, err, b.calls)
	}
}

func TestBuildCloudHooks_WiresKioskUnlock(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	spy := &kioskSpy{}
	dp.WindowCtl = spy
	setDisplayMode(t, dp, "self_order")
	h := buildCloudHooks(dp, nil)
	if h.KioskUnlock == nil {
		t.Fatal("buildCloudHooks leaves KioskUnlock nil")
	}
	if _, err := h.KioskUnlock(t.Context(), "d1", "s"); err != nil || spy.calls != 1 {
		t.Fatalf("hook: %v, calls=%d", err, spy.calls)
	}
}

// ADR-0142 D5: DeviceExtra reports self_order (from display.mode) and
// kiosk_pinned (what native code last pushed through SetKioskPinned).
func TestDeviceExtra_ReportsSelfOrderAndKioskPinned(t *testing.T) {
	t.Cleanup(func() { kiosk.SetKioskPinned(false) })
	dp := newCloudSyncTestDeps(t)
	h := buildCloudHooks(dp, nil)

	kiosk.SetKioskPinned(false)
	extra := h.DeviceExtra(t.Context())
	if extra["self_order"] != false || extra["kiosk_pinned"] != false {
		t.Fatalf("defaults: self_order=%#v kiosk_pinned=%#v", extra["self_order"], extra["kiosk_pinned"])
	}

	setDisplayMode(t, dp, "self_order")
	kiosk.SetKioskPinned(true)
	extra = h.DeviceExtra(t.Context())
	if extra["self_order"] != true || extra["kiosk_pinned"] != true {
		t.Fatalf("self-order pinned: self_order=%#v kiosk_pinned=%#v", extra["self_order"], extra["kiosk_pinned"])
	}

	setDisplayMode(t, dp, "till")
	extra = h.DeviceExtra(t.Context())
	if extra["self_order"] != false {
		t.Fatalf("till mode: self_order=%#v", extra["self_order"])
	}
}
