package pages

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#5069: when the till refused a staged restore at restart (the file
// was not a usable till database), Settings → Backups says so to a manager
// instead of leaving them wondering why nothing changed.
func TestSettingsPage_ShowsRefusedRestore(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	d.Cfg.DBPath = filepath.Join(t.TempDir(), "unitill-pos.db")
	live, err := db.Open(d.Cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	live.Close()

	if body := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(body, `data-testid="backup-restore-refused"`) {
		t.Fatal("refusal notice shown with nothing refused")
	}
	if err := db.StageRestoreFromReader(d.Cfg.DBPath, bytes.NewReader([]byte("not a database"))); err != nil {
		t.Fatal(err)
	}
	if applied, err := db.ApplyPendingRestore(d.Cfg.DBPath); applied || err != nil {
		t.Fatalf("apply: %v %v", applied, err)
	}
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	if !strings.Contains(body, `data-testid="backup-restore-refused"`) || !strings.Contains(body, "The last restore was not applied") {
		t.Fatalf("manager /settings lacks the refusal notice")
	}
	if body := getAs(mux, "/settings", &cashUser).Body.String(); strings.Contains(body, `data-testid="backup-restore-refused"`) {
		t.Fatal("cashier sees the restore notice")
	}
}
