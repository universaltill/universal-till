package mobilebench

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// RunJSON is the gomobile entry the iOS bench app calls with its bundle's
// directory of .wasm files; every file must be measured under its own name.
func TestRunJSONBenchesEveryWasmInDir(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "go-command.wasm"), "../guests/command")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a module"), 0o644); err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Platform map[string]string `json:"platform"`
		Results  []map[string]any  `json:"results"`
		Error    string            `json:"error"`
	}
	if err := json.Unmarshal([]byte(RunJSON(dir, 2, 5)), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Error != "" || len(rep.Results) == 0 {
		t.Fatalf("no results: %+v", rep)
	}
	for _, r := range rep.Results {
		if r["module"] != "go-command" || r["error"] != nil {
			t.Fatalf("unexpected row %+v", r)
		}
	}
}

// An empty bundle is reported, never an empty success the app would show.
func TestRunJSONReportsMissingModules(t *testing.T) {
	var rep struct{ Error string }
	if err := json.Unmarshal([]byte(RunJSON(t.TempDir(), 1, 1)), &rep); err != nil || rep.Error == "" {
		t.Fatalf("want an error for an empty dir, got %+v (%v)", rep, err)
	}
}
