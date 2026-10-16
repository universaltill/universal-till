package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func batchJSON(entity string, n int) []byte {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"external_id":"e%d","name":"N","price_minor":1}`, i)
	}
	return []byte(`{"entity":"` + entity + `","rows":[` + strings.Join(rows, ",") + `]}`)
}

// ADR-0161 §2 (ut-docs#4429): the per-run row buffer accepts batches only
// for the entities the run resolved, and refuses the whole run over a cap.
func TestImportRowSink_EntityGateAndCaps(t *testing.T) {
	s := NewImportRowSink("com.p", []string{"items"})
	if n, code := s.Submit(batchJSON("items", 3)); code != 0 || n != 3 {
		t.Fatalf("submit = %d, %d", n, code)
	}
	if _, code := s.Submit(batchJSON("customers", 1)); code != hostErrDenied {
		t.Fatalf("unresolved entity code = %d, want denied", code)
	}
	if _, code := s.Submit([]byte(`{"entity":"items","rows":{}}`)); code != hostErrInvalid {
		t.Fatalf("malformed batch code = %d, want invalid", code)
	}
	if s.Refused() != nil {
		t.Fatalf("a denied or malformed submit must not refuse the run: %v", s.Refused())
	}
	if got := len(s.Rows("items")); got != 3 {
		t.Fatalf("rows = %d, want 3", got)
	}

	// Over the per-call cap: the call fails and the run is refused.
	s = NewImportRowSink("com.p", []string{"items"})
	if _, code := s.Submit(batchJSON("items", ImportRowsPerCall+1)); code != hostErrQuota {
		t.Fatalf("per-call cap code = %d, want quota", code)
	}
	if !errors.Is(s.Refused(), ErrImportRowsCap) {
		t.Fatalf("run not refused after a per-call cap breach: %v", s.Refused())
	}
	if _, code := s.Submit(batchJSON("items", 1)); code != hostErrQuota {
		t.Fatalf("a refused run must refuse every later submit, got %d", code)
	}

	// Over the per-run row cap.
	s = NewImportRowSink("com.p", []string{"items"})
	for i := 0; i < ImportRowsPerRun/ImportRowsPerCall; i++ {
		if _, code := s.Submit(batchJSON("items", ImportRowsPerCall)); code != 0 {
			t.Fatalf("batch %d refused: %d", i, code)
		}
	}
	if _, code := s.Submit(batchJSON("items", 1)); code != hostErrQuota || s.Refused() == nil {
		t.Fatalf("row %d accepted (code %d)", ImportRowsPerRun+1, code)
	}

	// Over the per-run byte cap.
	s = NewImportRowSink("com.p", []string{"items"})
	huge := []byte(`{"entity":"items","rows":[{"external_id":"x","name":"` + strings.Repeat("a", ImportBytesPerRun) + `"}]}`)
	if _, code := s.Submit(huge); code != hostErrQuota || s.Refused() == nil {
		t.Fatalf("over the byte cap: code %d refused %v", code, s.Refused())
	}
}

// buildImportRowsGuest compiles testdata/importrows_guest once per call.
func buildImportRowsGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "importrows_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/importrows_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest: %v\n%s", err, raw)
	}
	return out
}

// TestImportRowsSubmit_RealWasmModule drives the real host function from a
// real compiled guest: rows reach the sink only during an import ask that
// carries a sink for this plugin; anything else is refused.
func TestImportRowsSubmit_RealWasmModule(t *testing.T) {
	guest := buildImportRowsGuest(t)
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.importrows"
	seedInstalledPlugin(t, db, pluginID, "ImportRows", "1.0.0", "wasm", true)
	for _, q := range []string{
		`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = '` + pluginID + `'`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES ('ir-perm', '` + pluginID + `', 'events:receive', 1)`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('ir-hook', '` + pluginID + `', 'import.requested.ask', 'import', 1)`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('ir-hook2', '` + pluginID + `', 'other.thing.ask', 'x', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	raw, err := os.ReadFile(guest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileWithParents(filepath.Join(base, pluginID, "1.0.0", "plugin.wasm"), raw); err != nil {
		t.Fatal(err)
	}
	w.Sync(ctx, db)
	bus := SharedBus(db)
	defer bus.ResetSubscribers()
	bus.SetEventMode("import.requested.ask", Blocking)
	bus.SetEventMode("other.thing.ask", Blocking)

	setSetting := func(k, v string) {
		t.Helper()
		b, _ := json.Marshal(v)
		if _, err := db.Exec(`DELETE FROM plugin_settings WHERE plugin_id = ? AND key = ?`, pluginID, k); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES (?, ?, ?, ?, 'global')`, pluginID+k, pluginID, k, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(ctx context.Context, event string) map[string]any {
		t.Helper()
		resp, ok, err := bus.AskPlugin(ctx, pluginID, event, map[string]any{"entry_key": "api", "entities": []string{"items"}, "mode": "preview", "source": "api"})
		if err != nil || !ok {
			t.Fatalf("ask %s: ok=%v err=%v", event, ok, err)
		}
		var out map[string]any
		if err := json.Unmarshal(resp, &out); err != nil {
			t.Fatalf("answer %s: %v", resp, err)
		}
		return out
	}

	// In an api-source import ask with a sink: 1,200 rows in three batches.
	setSetting("gen", "1200")
	sink := NewImportRowSink(pluginID, []string{"items"})
	if out := ask(WithImportRowSink(ctx, sink), "import.requested.ask"); out["ok"] != true {
		t.Fatalf("guest answer %v", out)
	}
	if got := len(sink.Rows("items")); got != 1200 {
		t.Fatalf("sink has %d rows, want 1200", got)
	}

	// Outside an api-source ask (no sink): denied.
	if out := ask(ctx, "import.requested.ask"); out["error"] != "import_rows_submit: -2" {
		t.Fatalf("no-sink answer %v, want denied", out)
	}
	// A sink on another event type is ignored: denied.
	if out := ask(WithImportRowSink(ctx, NewImportRowSink(pluginID, []string{"items"})), "other.thing.ask"); out["error"] != "import_rows_submit: -2" {
		t.Fatalf("other-event answer %v, want denied", out)
	}
	// A sink for another plugin is never this plugin's.
	if out := ask(WithImportRowSink(ctx, NewImportRowSink("com.someone.else", []string{"items"})), "import.requested.ask"); out["error"] != "import_rows_submit: -2" {
		t.Fatalf("foreign-sink answer %v, want denied", out)
	}
	// An entity the run did not resolve: denied.
	setSetting("entity", "customers")
	if out := ask(WithImportRowSink(ctx, NewImportRowSink(pluginID, []string{"items"})), "import.requested.ask"); out["error"] != "import_rows_submit: -2" {
		t.Fatalf("unresolved-entity answer %v, want denied", out)
	}
	// Over the per-call cap: quota, and the run is refused.
	setSetting("entity", "items")
	setSetting("batch", "501")
	sink = NewImportRowSink(pluginID, []string{"items"})
	if out := ask(WithImportRowSink(ctx, sink), "import.requested.ask"); out["error"] != "import_rows_submit: -5" {
		t.Fatalf("per-call cap answer %v, want quota", out)
	}
	if sink.Refused() == nil {
		t.Fatal("run not refused after a cap breach")
	}
}
