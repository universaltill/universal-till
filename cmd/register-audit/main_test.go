package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func TestRunPrintsTableAndJSON(t *testing.T) {
	path := testsupport.MigratedDBFile(t, "cli.db")
	w, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.DB.Exec(`INSERT INTO registers (id, name, is_active) VALUES ('reg-default','Main',1),('r1','phone',1)`); err != nil {
		t.Fatal(err)
	}
	w.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"--db", path}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "register-audit (read-only)") || !strings.Contains(out.String(), "phone") {
		t.Fatalf("table output: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"--db", path, "--format", "json"}, &out, &errb); code != 0 || !strings.Contains(out.String(), `"register_id": "r1"`) {
		t.Fatalf("json output (exit %d): %s", code, out.String())
	}
	if code := run([]string{}, &out, &errb); code != 2 {
		t.Fatalf("no --db: exit %d, want 2", code)
	}
	if code := run([]string{"--db", path + ".missing"}, &out, &errb); code != 1 {
		t.Fatalf("missing db: exit %d, want 1", code)
	}
}
