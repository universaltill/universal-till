package plugins

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/plugins/tax-tr/okc/sim"
)

// The real ut-plugin-tax-tr (plugins/tax-tr) compiled to wasip1 and run
// through this runtime against the simulated device: proves the whole
// "pay on the device" tender leg — settings_get for the device address,
// tcp_* to reach it, the bridge protocol, and the `fiscal_device` answer
// core persists — before any certified device exists (ut-docs#1280).

func buildOKCPlugin(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "tax-tr.wasm")
	cmd := exec.Command("go", "build", "-o", out, "../../plugins/tax-tr")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 tax-tr plugin: %v\n%s", err, raw)
	}
	return out
}

func startOKCSim(t *testing.T, opts sim.Options) *sim.Server {
	t.Helper()
	s, err := sim.Start("127.0.0.1:0", opts)
	if err != nil {
		t.Fatalf("start okc sim: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedOKCSettings(t *testing.T, d *data.PluginRepo, pluginID string, port int) {
	t.Helper()
	ctx := context.Background()
	for k, v := range map[string]string{
		"okc.driver":             `"bridge"`,
		"okc.host":               `"127.0.0.1"`,
		"okc.port":               `"` + itoa(port) + `"`,
		"okc.maker":              `"sim"`,
		"okc.connect_timeout_ms": `"1000"`,
		"okc.read_timeout_ms":    `"1500"`,
	} {
		if err := d.UpsertPluginSettingScoped(ctx, pluginID, k, v, "global"); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func authorizePayload(amount, total int64) map[string]any {
	return map[string]any{
		"method": "okc", "amount": amount, "reference": "", "plugin_id": fiscal.PluginIDTaxTR,
		"currency": "TRY", "total": total, "tax_inclusive": true, "sale_discount": 0, "service_charge": 0,
		"lines": []map[string]any{{"name": "Çay", "qty": 2, "unit_price": 1500, "tax_rate_bp": 1000, "line_discount": 0}},
	}
}

func runOKCEvent(t *testing.T, w *WasmRuntime, d *data.PluginRepo, db interface{}, pluginID, evType string, payload map[string]any) (json.RawMessage, error) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	ev := Event{ID: "ev-" + evType, Type: evType, Timestamp: time.Now(), Payload: raw}
	w.mu.Lock()
	w.hasTCP[pluginID] = true
	w.mu.Unlock()
	return w.HandleEvent(context.Background(), pluginID, ev)
}

func TestOKCPlugin_AuthorizeReturnsDeviceEvidence(t *testing.T) {
	guest := buildOKCPlugin(t)
	db := hostfnTestDB(t)
	const pluginID = fiscal.PluginIDTaxTR
	seedPlugin(t, db, pluginID)
	grantPerm(t, db, pluginID, "tcp:*")
	s := startOKCSim(t, sim.Options{Serial: "SIM-TEST-1", Maker: "sim", ZNo: 4})
	repo := data.NewPluginRepo(db)
	seedOKCSettings(t, repo, pluginID, s.Port())

	w := newTCPTestRuntime(t, guest, pluginID)
	w.mu.Lock()
	w.db = db
	w.mu.Unlock()

	resp, err := runOKCEvent(t, w, repo, db, pluginID, "payment.okc.authorize", authorizePayload(3000, 3000))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	ev, ok := fiscal.ParseDeviceEvidence(resp)
	if !ok {
		t.Fatalf("no fiscal_device evidence in answer: %s", resp)
	}
	if ev.Serial != "SIM-TEST-1" || ev.Maker != "sim" || ev.ReceiptNo != "0000001" || ev.ReceiptKind != "mali_fis" || ev.ZNo != 4 {
		t.Fatalf("evidence = %+v", ev)
	}
	log := s.Log()
	if len(log) != 1 || log[0].Amount != 3000 || log[0].Lines != 1 || log[0].RequestID != "ev-payment.okc.authorize" {
		t.Fatalf("device log = %+v", log)
	}

	// Refund leg prints a refund slip.
	resp, err = runOKCEvent(t, w, repo, db, pluginID, "payment.okc.refund", map[string]any{
		"method": "okc", "amount": 1500, "currency": "TRY", "original_sale_id": "s1", "original_receipt": "0000001", "plugin_id": pluginID,
	})
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if ev, ok := fiscal.ParseDeviceEvidence(resp); !ok || ev.ReceiptKind != "iade_fisi" || ev.ReceiptNo != "0000002" {
		t.Fatalf("refund evidence = %+v ok=%v", ev, ok)
	}

	// Settle notification is a no-op, never a decline.
	if _, err := runOKCEvent(t, w, repo, db, pluginID, "payment.okc.requested", map[string]any{"sale_id": "s1"}); err != nil {
		t.Fatalf("requested must never fail: %v", err)
	}
}

// Fail closed: a declining device, a silent device and a missing device
// each refuse the tender (non-zero exit → HandleEvent error → no sale row),
// and nothing is printed.
func TestOKCPlugin_RefusesTenderWhenDeviceCannotPrint(t *testing.T) {
	guest := buildOKCPlugin(t)
	db := hostfnTestDB(t)
	const pluginID = fiscal.PluginIDTaxTR
	seedPlugin(t, db, pluginID)
	grantPerm(t, db, pluginID, "tcp:*")
	repo := data.NewPluginRepo(db)
	w := newTCPTestRuntime(t, guest, pluginID)
	w.mu.Lock()
	w.db = db
	w.mu.Unlock()

	t.Run("declined", func(t *testing.T) {
		s := startOKCSim(t, sim.Options{DeclineAll: true})
		seedOKCSettings(t, repo, pluginID, s.Port())
		if _, err := runOKCEvent(t, w, repo, db, pluginID, "payment.okc.authorize", authorizePayload(3000, 3000)); err == nil {
			t.Fatal("a declining device must refuse the tender")
		}
		if n := len(s.Log()); n != 0 {
			t.Fatalf("declined tender printed %d receipts", n)
		}
	})
	t.Run("split tender", func(t *testing.T) {
		s := startOKCSim(t, sim.Options{})
		seedOKCSettings(t, repo, pluginID, s.Port())
		if _, err := runOKCEvent(t, w, repo, db, pluginID, "payment.okc.authorize", authorizePayload(1000, 3000)); err == nil {
			t.Fatal("a split tender must be refused")
		}
		if n := len(s.Log()); n != 0 {
			t.Fatalf("split tender reached the device: %d receipts", n)
		}
	})
	t.Run("silent", func(t *testing.T) {
		s := startOKCSim(t, sim.Options{Silent: true})
		seedOKCSettings(t, repo, pluginID, s.Port())
		start := time.Now()
		if _, err := runOKCEvent(t, w, repo, db, pluginID, "payment.okc.authorize", authorizePayload(3000, 3000)); err == nil {
			t.Fatal("a silent device must refuse the tender")
		}
		if time.Since(start) > 8*time.Second {
			t.Fatalf("silent device held the tender for %s", time.Since(start))
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		s := startOKCSim(t, sim.Options{})
		port := s.Port()
		s.Close()
		seedOKCSettings(t, repo, pluginID, port)
		if _, err := runOKCEvent(t, w, repo, db, pluginID, "payment.okc.authorize", authorizePayload(3000, 3000)); err == nil {
			t.Fatal("an unreachable device must refuse the tender")
		}
	})
	t.Run("scaffold driver", func(t *testing.T) {
		s := startOKCSim(t, sim.Options{})
		seedOKCSettings(t, repo, pluginID, s.Port())
		if err := repo.UpsertPluginSettingScoped(context.Background(), pluginID, "okc.driver", `"gmp3"`, "global"); err != nil {
			t.Fatal(err)
		}
		if _, err := runOKCEvent(t, w, repo, db, pluginID, "payment.okc.authorize", authorizePayload(3000, 3000)); err == nil {
			t.Fatal("an unfinished maker driver must refuse the tender, never pretend")
		}
		if n := len(s.Log()); n != 0 {
			t.Fatalf("scaffold driver reached the device: %d receipts", n)
		}
	})
}

// Without the tcp:* grant the plugin cannot reach the device and must
// refuse — the permission model, not the plugin, decides who may dial.
func TestOKCPlugin_RefusesWithoutTCPGrant(t *testing.T) {
	guest := buildOKCPlugin(t)
	db := hostfnTestDB(t)
	const pluginID = fiscal.PluginIDTaxTR
	seedPlugin(t, db, pluginID)
	grantPerm(t, db, pluginID, "storage")
	s := startOKCSim(t, sim.Options{})
	repo := data.NewPluginRepo(db)
	seedOKCSettings(t, repo, pluginID, s.Port())
	w := newTCPTestRuntime(t, guest, pluginID)
	w.mu.Lock()
	w.db = db
	w.mu.Unlock()
	if _, err := runOKCEvent(t, w, repo, db, pluginID, "payment.okc.authorize", authorizePayload(3000, 3000)); err == nil {
		t.Fatal("without tcp:* the tender must be refused")
	}
	if n := len(s.Log()); n != 0 {
		t.Fatalf("device saw %d receipts without a tcp grant", n)
	}
}
