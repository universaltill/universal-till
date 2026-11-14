package cloudsync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// ut-docs#5614: a directive that changes this till's own device report is
// followed, in the same tick, by a second /v1/stores/sync POST carrying the
// applied state (contract §3.6), hash-gated.

const negInv = "pos.allow_negative_inventory"

// settingHooks is a till whose one till setting is val: SetTillSetting writes
// it, DeviceExtra reports it fresh on every build.
type settingHooks struct {
	mu  sync.Mutex
	val string
}

func (s *settingHooks) get() string { s.mu.Lock(); defer s.mu.Unlock(); return s.val }

func (s *settingHooks) hooks(onSet func()) Hooks {
	return Hooks{
		SetTillSetting: func(ctx context.Context, key, value string) (string, error) {
			s.mu.Lock()
			s.val = value
			s.mu.Unlock()
			if onSet != nil {
				onSet()
			}
			return key + " = " + value, nil
		},
		DeviceExtra: func(ctx context.Context) map[string]any {
			return map[string]any{"till_settings": map[string]any{negInv: s.get()}}
		},
	}
}

func setTillDirective(id, value string) map[string]any {
	return map[string]any{"id": id, "type": "set_till_setting", "payload": map[string]any{"key": negInv, "value": value}}
}

// reportedSetting reads till_settings[negInv] of the first device in a recorded
// sync body.
func reportedSetting(t *testing.T, body map[string]any) string {
	t.Helper()
	devs, _ := body["devices"].([]any)
	if len(devs) == 0 {
		t.Fatalf("body has no devices: %+v", body)
	}
	dev, _ := devs[0].(map[string]any)
	ts, _ := dev["till_settings"].(map[string]any)
	v, _ := ts[negInv].(string)
	return v
}

func resultStatuses(c *fakeCloud) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	got := map[string]string{}
	for _, r := range c.results {
		got[r["directive_id"]] = r["status"]
	}
	return got
}

func TestTick_AppliedTillSettingReportedInSameTick(t *testing.T) {
	cloud := &fakeCloud{directives: []map[string]any{setTillDirective("d1", "true")}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	st := &settingHooks{val: "false"}

	if err := Tick(context.Background(), testCfg(srv.URL), db, st.hooks(nil)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n := len(cloud.syncBodies); n != 2 {
		t.Fatalf("sync POSTs = %d, want 2 (report + same-tick follow-up)", n)
	}
	if got := reportedSetting(t, cloud.syncBodies[0]); got != "false" {
		t.Fatalf("first report = %q, want the pre-apply value false", got)
	}
	if got := reportedSetting(t, cloud.syncBodies[1]); got != "true" {
		t.Fatalf("follow-up report = %q, want the applied value true", got)
	}
	if got := resultStatuses(cloud)["d1"]; got != "applied" {
		t.Fatalf("d1 result = %q, want applied", got)
	}
}

func TestTick_NoExtraPostWithoutReportChange(t *testing.T) {
	cases := []struct {
		name string
		dirs []map[string]any
		hook func(st *settingHooks) Hooks
	}{
		{"no directives", nil, func(st *settingHooks) Hooks { return st.hooks(nil) }},
		{"catalogue only", []map[string]any{
			{"id": "c1", "type": "set_price", "payload": map[string]any{"item_id": "itm-1", "price_minor": 250}},
		}, func(st *settingHooks) Hooks {
			h := st.hooks(nil)
			h.SetPrice = func(ctx context.Context, id string, minor int64) (string, error) { return "ok", nil }
			return h
		}},
		{"set_till_setting failed", []map[string]any{setTillDirective("f1", "true")}, func(st *settingHooks) Hooks {
			h := st.hooks(nil)
			h.SetTillSetting = func(ctx context.Context, k, v string) (string, error) { return "", errors.New("refused") }
			return h
		}},
		{"applied but value unchanged", []map[string]any{setTillDirective("u1", "false")}, func(st *settingHooks) Hooks { return st.hooks(nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cloud := &fakeCloud{directives: tc.dirs}
			srv := httptest.NewServer(cloud.handler())
			defer srv.Close()
			db := testDB(t)
			st := &settingHooks{val: "false"}
			if err := Tick(context.Background(), testCfg(srv.URL), db, tc.hook(st)); err != nil {
				t.Fatalf("tick: %v", err)
			}
			if n := len(cloud.syncBodies); n != 1 {
				t.Fatalf("sync POSTs = %d, want exactly 1", n)
			}
		})
	}
}

func TestTick_DeviceReportFollowUpFailureRetriedNextTick(t *testing.T) {
	fakeClock(t)
	cloud, srv := newCheckinCloud(1)
	defer srv.Close()
	cloud.fakeCloud.directives = []map[string]any{setTillDirective("d1", "true")}
	cloud.fakeCloud.syncStatus = func(n int) int {
		if n == 2 {
			return http.StatusInternalServerError
		}
		return 0
	}
	db := testDB(t)
	st := &settingHooks{val: "false"}
	hooks := st.hooks(nil)

	mustTick(t, srv.URL, db, hooks) // a failed follow-up never fails the tick
	if got := resultStatuses(cloud.fakeCloud)["d1"]; got != "applied" {
		t.Fatalf("d1 result = %q, want applied", got)
	}
	if n := cloud.posts(); n != 2 {
		t.Fatalf("after tick 1 POSTs = %d, want 2", n)
	}
	mustTick(t, srv.URL, db, hooks) // GET answers 304, state hash not recorded → POST
	if n := cloud.posts(); n != 3 {
		t.Fatalf("after tick 2 POSTs = %d, want 3 (retry carries the value)", n)
	}
	cloud.fakeCloud.mu.Lock()
	last := cloud.fakeCloud.syncBodies[2]
	cloud.fakeCloud.mu.Unlock()
	if got := reportedSetting(t, last); got != "true" {
		t.Fatalf("retry report = %q, want true", got)
	}
}

func TestTick_DeviceReportFollowUpRecordsStateHash(t *testing.T) {
	fakeClock(t)
	cloud, srv := newCheckinCloud(1)
	defer srv.Close()
	cloud.fakeCloud.directives = []map[string]any{setTillDirective("d1", "true")}
	db := testDB(t)
	st := &settingHooks{val: "false"}
	hooks := st.hooks(nil)

	mustTick(t, srv.URL, db, hooks)
	if n := cloud.posts(); n != 2 {
		t.Fatalf("after tick 1 POSTs = %d, want 2", n)
	}
	mustTick(t, srv.URL, db, hooks) // 304, report unchanged since the follow-up
	if n := cloud.posts(); n != 2 {
		t.Fatalf("after tick 2 POSTs = %d, want still 2 (no redundant POST)", n)
	}
}

func TestTick_DeviceReportFollowUpDropsDirectives(t *testing.T) {
	cloud := &fakeCloud{directives: []map[string]any{setTillDirective("d1", "true")}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	st := &settingHooks{val: "false"}
	hooks := st.hooks(func() {
		// Queued while the first answer is being applied: only the
		// follow-up's answer can carry it.
		cloud.mu.Lock()
		cloud.directives = []map[string]any{{"id": "d9", "type": "set_setting", "payload": map[string]any{"key": "display.osk", "value": "on"}}}
		cloud.mu.Unlock()
	})
	setCalled := false
	hooks.SetSetting = func(ctx context.Context, k, v string) (string, error) { setCalled = true; return "ok", nil }

	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n := len(cloud.syncBodies); n != 2 {
		t.Fatalf("sync POSTs = %d, want 2 (no third)", n)
	}
	if setCalled {
		t.Fatal("a directive answered to the follow-up must not be applied")
	}
	if _, ok := resultStatuses(cloud)["d9"]; ok {
		t.Fatal("no result may be posted for a directive the follow-up answer carried")
	}
}

func TestTick_DeviceReportFollowUpOnSatellite(t *testing.T) {
	cloud := &fakeCloud{directives: []map[string]any{setTillDirective("d1", "true")}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url','http://10.0.0.2:8080')`); err != nil {
		t.Fatal(err)
	}
	st := &settingHooks{val: "false"}
	if err := Tick(context.Background(), testCfg(srv.URL), db, st.hooks(nil)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n := len(cloud.syncBodies); n != 2 {
		t.Fatalf("satellite sync POSTs = %d, want 2 (any till that applied it re-reports)", n)
	}
}

func TestTick_DeviceReportFollowUp402DoesNotFailTick(t *testing.T) {
	cloud := &fakeCloud{directives: []map[string]any{setTillDirective("d1", "true")}}
	cloud.syncStatus = func(n int) int {
		if n == 2 {
			return http.StatusPaymentRequired
		}
		return 0
	}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := testDB(t)
	st := &settingHooks{val: "false"}
	if err := Tick(context.Background(), testCfg(srv.URL), db, st.hooks(nil)); err != nil {
		t.Fatalf("tick must not fail on a mid-tick 402: %v", err)
	}
	if n := len(cloud.syncBodies); n != 2 {
		t.Fatalf("sync POSTs = %d, want 2 and no retry", n)
	}
}
