package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#1728 (independent review, finding 4). The half of this fix that
// repairs tills ALREADY IN THE FIELD is the boot-time adoption pass, and
// until this test existed it had no coverage of its own being wired in at
// all: deleting the AdoptDefaultPrinterCharset call from Run left the whole
// suite green. This boots the real thing against a real data directory and
// asserts the stored charset actually moved.
func TestRun_AdoptsPrinterCharsetDefaultOnBoot(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "unitill-pos.db")

	// A till already in the field: set up as a German EUR shop, with the
	// printer configured at some point, so "utf8" is stored explicitly and
	// the read-time default can never fire for it.
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	st := settings.NewStore(seed.DB)
	ctx := context.Background()
	for k, v := range map[string]string{
		"setup.completed": "true",
		"store.currency":  "EUR",
		"store.locale":    "de-DE",
		"printer.charset": "utf8",
	} {
		if err := st.Set(ctx, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	t.Setenv("UT_DATA_DIR", dataDir)
	t.Setenv("UT_ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist.env"))
	t.Setenv("UT_AUTH", "off")
	t.Setenv("UT_OPEN_BROWSER", "false")
	t.Setenv("UT_LISTEN_ADDR", "127.0.0.1:0")

	bootReachedPagesInit := observePagesInit(t)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(runCtx) }()

	select {
	case <-bootReachedPagesInit:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not reach pages.Init within 30s")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return within 30s of ctx cancellation")
	}

	after, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer after.Close()
	got, _, err := settings.NewStore(after.DB).Get(ctx, "printer.charset")
	if err != nil {
		t.Fatalf("read printer.charset: %v", err)
	}
	if got != "cp858" {
		t.Fatalf("printer.charset after boot = %q, want cp858 — the boot-time "+
			"adoption pass is not wired into Run, so a till already in the "+
			"field keeps printing mojibake for its currency symbol", got)
	}
}
