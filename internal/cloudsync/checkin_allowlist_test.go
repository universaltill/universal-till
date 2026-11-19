package cloudsync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
)

// ut-docs#6613 / #6618 (PR #2581): the check-in of an auto-registered, unpaid
// till, and of a till whose shop pressed "Stop sending", is built from an
// ALLOW-LIST. The golden list below is the only thing that may ride the device
// record besides `events`; a new key fails this test and forces a review of
// the notice text and enroll.NoticeVersion.
var goldenMinimalDeviceKeys = []string{
	"approval_kid", "channel", "country", "device_id", "health", "locale", "platform", "version",
}

// fatHooks is the shape pages.buildCloudHooks really returns (the real hook is
// pinned by internal/pages/checkin_payload_test.go).
func fatHooks() Hooks {
	return Hooks{DeviceExtra: func(context.Context) map[string]any {
		return map[string]any{
			"store_name":    "Corner Shop",
			"users":         []map[string]any{{"username": "maria", "display_name": "Maria K.", "role": "cashier", "has_pin": true}},
			"roles":         []string{"cashier"},
			"till_settings": map[string]string{"receipt.footer": "free text the owner typed", "invoice.seller_vat_no": "GB123456789"},
			"lan_address":   "192.168.1.30:37673",
			"categories":    []map[string]any{{"name": "Hot drinks"}},
			"quick_buttons": []map[string]any{{"barcode": "5000000000001", "label": "Flat white"}},
			"peripherals":   []string{"printer"},
			"country":       "GB",
			"locale":        "en-GB",
			"channel":       "stable",
			"currency":      "GBP",
		}
	}}
}

func deviceKeysOf(t *testing.T, body []byte) []string {
	t.Helper()
	var req struct {
		Devices []map[string]any `json:"devices"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.Devices) != 1 {
		t.Fatalf("body: %v %s", err, body)
	}
	keys := make([]string, 0, len(req.Devices[0]))
	for k := range req.Devices[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func assertWithinAllowList(t *testing.T, keys []string) {
	t.Helper()
	allowed := map[string]bool{"events": true}
	for _, k := range goldenMinimalDeviceKeys {
		allowed[k] = true
	}
	for _, k := range keys {
		if !allowed[k] {
			t.Errorf("device report carries %q, which is not on the allow-list %v", k, goldenMinimalDeviceKeys)
		}
	}
}

func captureServer(t *testing.T, status int) (*httptest.Server, func() [][]byte) {
	t.Helper()
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"plan_required"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() [][]byte { mu.Lock(); defer mu.Unlock(); return append([][]byte(nil), bodies...) }
}

func TestAutoRegisteredUnpaidStartCheckinCarriesOnlyAllowListedKeys(t *testing.T) {
	useRealSyncGate(t)
	resetTillAuth(t)
	pinOperatorNow(t, startupDay)
	pinVersion(t, "0.30.20")
	srv, got := captureServer(t, http.StatusPaymentRequired)
	db := testDB(t)
	ctx := context.Background()
	if err := data.NewSettingsRepo(db).SetMany(ctx, map[string]string{
		"setup.completed":        "true",
		enroll.KeyNoticeShownAt:  time.Now().UTC().Format(time.RFC3339),
		enroll.KeyNoticeVersion:  enroll.NoticeVersion,
		enroll.KeyAutoRegistered: "true",
		"till.name":              "Front Counter",
	}); err != nil {
		t.Fatal(err)
	}
	armStartup()
	_, _ = tick(ctx, testCfg(srv.URL), db, fatHooks())
	bodies := got()
	if len(bodies) != 1 {
		t.Fatalf("POSTs = %d, want 1", len(bodies))
	}
	keys := deviceKeysOf(t, bodies[0])
	assertWithinAllowList(t, keys)
	for _, must := range []string{"device_id", "version", "platform"} {
		found := false
		for _, k := range keys {
			found = found || k == must
		}
		if !found {
			t.Errorf("allow-listed key %q missing from %v", must, keys)
		}
	}
}

// #6618: a till that pressed "Stop sending" and then does something that opens
// the operator window (claim code, check for a plan) sends the minimal report,
// never the full one.
func TestStoppedTillOperatorWindowSendsMinimalReport(t *testing.T) {
	useRealSyncGate(t)
	resetTillAuth(t)
	pinOperatorNow(t, startupDay)
	pinVersion(t, "0.30.20")
	srv, got := captureServer(t, http.StatusPaymentRequired)
	db := testDB(t)
	ctx := context.Background()
	if err := data.NewSettingsRepo(db).SetMany(ctx, map[string]string{
		"setup.completed":        "true",
		enroll.KeyNoticeShownAt:  time.Now().UTC().Format(time.RFC3339),
		enroll.KeyNoticeVersion:  enroll.NoticeVersion,
		enroll.KeyAutoRegistered: "true",
		enroll.KeyStopped:        "true",
	}); err != nil {
		t.Fatal(err)
	}
	armStartup()
	_, _ = tick(ctx, testCfg(srv.URL), db, fatHooks())
	if n := len(got()); n != 0 {
		t.Fatalf("stopped till made a start-up POST (%d)", n)
	}
	prev := operatorDeadlineNS.Load()
	t.Cleanup(func() { operatorDeadlineNS.Store(prev) })
	OpenOperatorWindow(2 * time.Minute)
	_, _ = tick(ctx, testCfg(srv.URL), db, fatHooks())
	bodies := got()
	if len(bodies) != 1 {
		t.Fatalf("operator window POSTs = %d, want 1", len(bodies))
	}
	assertWithinAllowList(t, deviceKeysOf(t, bodies[0]))
}

// The golden list in this file and the shipped allow-list must be the same
// set (health is cut to uptime_min and checked below).
func TestMinimalDeviceKeysMatchGolden(t *testing.T) {
	want := append([]string(nil), goldenMinimalDeviceKeys...)
	want = removeKey(want, "health")
	got := append([]string(nil), minimalDeviceKeys...)
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		t.Fatalf("allow-list %v != golden %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("allow-list %v != golden %v", got, want)
		}
	}
	out := minimalDeviceReport(map[string]any{
		"device_id": "d", "users": 1,
		"health": map[string]any{"uptime_min": 3, "db_mb": 9},
	})
	h, _ := out["health"].(map[string]any)
	if len(out) != 2 || len(h) != 1 || h["uptime_min"] != 3 {
		t.Fatalf("minimal report = %v", out)
	}
}

func removeKey(in []string, k string) []string {
	out := in[:0]
	for _, v := range in {
		if v != k {
			out = append(out, v)
		}
	}
	return out
}
