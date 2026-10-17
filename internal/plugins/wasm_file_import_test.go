package plugins

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sdk "github.com/universaltill/universal-till/sdk/plugin"
)

// ADR-0164 §2 (ut-docs#4526): a file-rows import ask runs in the import
// sandbox — the plugin reads the staged file and submits rows, and every
// other host function answers denied without acting.

// FileImportHostFunctions must equal the guest SDK's copy, which is what a
// plugin author tests against (FakeHost.FileImport) and what ut-cloud's
// import host mirrors.
func TestFileImportHostFunctions_MirrorSDK(t *testing.T) {
	got := append([]string(nil), FileImportHostFunctions...)
	want := append([]string(nil), sdk.FileImportHostFunctions...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("core %v, SDK %v", got, want)
	}
}

// Only the sandbox's own host functions may read the host state past the
// sandbox (stateFromAny); every other one goes through stateFrom, which
// refuses inside a file-rows ask. A new host function is therefore denied
// in the sandbox unless it is deliberately added here.
func TestFileImportSandbox_OnlyAllowedFunctionsBypass(t *testing.T) {
	exported := map[string]string{} // Go func → export name
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	callers := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Export" && len(call.Args) == 1 {
				if inner, ok := sel.X.(*ast.CallExpr); ok {
					if isel, ok := inner.Fun.(*ast.SelectorExpr); ok && isel.Sel.Name == "WithFunc" && len(inner.Args) == 1 {
						if id, ok := inner.Args[0].(*ast.Ident); ok {
							if lit, ok := call.Args[0].(*ast.BasicLit); ok {
								exported[id.Name] = strings.Trim(lit.Value, `"`)
							}
						}
					}
				}
			}
			return true
		})
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Name.Name == "stateFrom" || fd.Name.Name == "stateErr" {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "stateFromAny" {
						callers[fd.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	if len(exported) < 30 {
		t.Fatalf("parsed only %d host exports — the parser no longer matches instantiateHostModule", len(exported))
	}
	allowed := map[string]bool{}
	for _, n := range FileImportHostFunctions {
		allowed[n] = true
	}
	for fn := range callers {
		name, isExport := exported[fn]
		if !isExport || !allowed[name] {
			t.Errorf("%s calls stateFromAny but is not a file-import host function (%q)", fn, name)
		}
	}
	for fn, name := range exported {
		if allowed[name] && !callers[fn] {
			t.Errorf("%s (%s) is in FileImportHostFunctions but does not use stateFromAny, so the sandbox refuses it", fn, name)
		}
	}
}

func buildFileRowsGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "filerows_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/filerows_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest: %v\n%s", err, raw)
	}
	return out
}

// A real compiled guest in a file-rows ask: it reads the file and submits
// rows; settings, storage, HTTP, events, device info and blobs are all
// denied (-2) even with every permission granted. The same plugin in a
// legacy file ask (no file sink) is not sandboxed, and its submit is
// denied as before.
func TestFileImportSandbox_RealWasmModule(t *testing.T) {
	guest := buildFileRowsGuest(t)
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)
	defer w.Close(ctx)

	const pluginID = "com.test.filerows"
	seedInstalledPlugin(t, db, pluginID, "FileRows", "1.0.0", "wasm", true)
	for _, q := range []string{
		`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = '` + pluginID + `'`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('fr-hook', '` + pluginID + `', 'import.requested.ask', 'import', 1)`,
		`INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('fr-s', '` + pluginID + `', 'anything', '"secret"', 'global')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for i, p := range []string{"events:receive", "storage", "net:probe.invalid", "events:publish", "device-info", "blob:own"} {
		if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, ?, 1)`, "fr-p"+string(rune('a'+i)), pluginID, p); err != nil {
			t.Fatal(err)
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

	ask := func(ctx context.Context, content string) map[string]any {
		t.Helper()
		path := filepath.Join(t.TempDir(), "upload.csv")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		h, err := OpenImportFile(pluginID, path)
		if err != nil {
			t.Fatal(err)
		}
		defer CloseImportFile(pluginID, h)
		resp, ok, err := bus.AskPlugin(ctx, pluginID, "import.requested.ask", map[string]any{
			"entry_key": "export", "entities": []string{"items"}, "file_handle": h, "file_name": "upload.csv",
			"file_size": len(content), "mode": "preview", "source": "file"})
		if err != nil || !ok {
			t.Fatalf("ask: ok=%v err=%v", ok, err)
		}
		var out map[string]any
		if err := json.Unmarshal(resp, &out); err != nil {
			t.Fatalf("answer %s: %v", resp, err)
		}
		return out
	}
	const file = "fixture_ref,fixture_title,fixture_price_minor\nr1,Tea,250\nr2,Scone,300\n"

	sink := NewFileImportRowSink(pluginID, []string{"items"})
	out := ask(WithImportRowSink(ctx, sink), file+"x,PROBE,0\n")
	if out["ok"] != true {
		t.Fatalf("guest answer %v", out)
	}
	msg, _ := out["message"].(string)
	for _, fn := range []string{"settings_get", "storage_get", "http_request", "event_publish", "device_timezone_get", "blob_list"} {
		if !strings.Contains(msg, fn+"=-2") {
			t.Errorf("%s not denied in the sandbox: %s", fn, msg)
		}
	}
	if got := len(sink.Rows("items")); got != 2 {
		t.Fatalf("sink rows = %d, want 2", got)
	}

	// The same guest outside a file-rows run: not sandboxed, and its rows
	// are refused (a legacy file ask has no row sink, ADR-0161 unchanged).
	out = ask(ctx, file+"x,PROBE,0\n")
	if out["error"] != "import_rows_submit: -2" {
		t.Fatalf("legacy file ask answer %v, want the submit denied", out)
	}
	// An API-source sink does not sandbox (ADR-0161 entries keep their
	// network and settings).
	apiSink := NewImportRowSink(pluginID, []string{"items"})
	out = ask(WithImportRowSink(ctx, apiSink), file+"x,PROBE,0\n")
	if msg, _ := out["message"].(string); strings.Contains(msg, "settings_get=-2") {
		t.Fatalf("api-source run was sandboxed: %s", msg)
	}

	// ADR-0161's caps hold in a file-rows run too.
	sink = NewFileImportRowSink(pluginID, []string{"items"})
	if out := ask(WithImportRowSink(ctx, sink), file+"x,BATCH:501,0\nx,GEN:501,0\n"); out["error"] != "import_rows_submit: -5" {
		t.Fatalf("per-call cap answer %v, want quota", out)
	}
	if sink.Refused() == nil {
		t.Fatal("run not refused after a cap breach")
	}
}
