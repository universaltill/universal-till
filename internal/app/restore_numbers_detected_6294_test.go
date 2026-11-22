package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#6294: a main till whose data folder was restored onto a new install
// (DB install id present, secrets/install-id missing) owes the number jump on
// that same boot, exactly once.

type restoredBoot struct {
	t      *testing.T
	dbPath string
	d      *db.DB
	store  *settings.Store
	log    *recordingLog
}

func newRestoredBoot(t *testing.T) *restoredBoot {
	t.Helper()
	orig := paths.DataDir()
	dir := t.TempDir()
	paths.Init(dir)
	t.Cleanup(func() { paths.Init(orig) })
	dbPath := filepath.Join(dir, "unitill-pos.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	b := &restoredBoot{t: t, dbPath: dbPath, d: d, store: settings.NewStore(d.DB), log: &recordingLog{}}
	// The restored folder: install id A in the DB, no secrets/install-id.
	b.exec(`INSERT INTO settings (key, value) VALUES ('till_identity.install_id', 'install-A')`)
	now := time.Now().UTC()
	b.exec(`INSERT INTO sales (id, receipt_no, subtotal, total, created_at) VALUES ('s42', '000000042', 100, 100, ?)`,
		now.AddDate(0, 0, -2).Format(time.RFC3339))
	b.exec(`INSERT INTO report_archive (id, kind, period, content_json, z_number, created_at) VALUES ('ra3', 'eod', 'p3', '{}', 3, ?)`,
		now.AddDate(0, 0, -2).Format("2006-01-02T15:04:05.000Z"))
	return b
}

func (b *restoredBoot) exec(q string, args ...any) {
	b.t.Helper()
	if _, err := b.d.DB.Exec(q, args...); err != nil {
		b.t.Fatal(err)
	}
}

func (b *restoredBoot) setting(k string) string {
	b.t.Helper()
	var v string
	if err := b.d.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, k).Scan(&v); err != nil && !errors.Is(err, sql.ErrNoRows) {
		b.t.Fatal(err)
	}
	return v
}

func (b *restoredBoot) audits() int {
	b.t.Helper()
	var n int
	if err := b.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'restore_numbers_jumped'`).Scan(&n); err != nil {
		b.t.Fatal(err)
	}
	return n
}

func (b *restoredBoot) boot(cfg *config.Config, kv enroll.Settings) {
	b.t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.DBPath = b.dbPath
	if kv == nil {
		kv = b.store
	}
	bootRestoreChecks(context.Background(), cfg, b.d.DB, kv, time.Now(), b.log)
}

func (b *restoredBoot) floors() string {
	return fmt.Sprintf("receipt=%s z=%s token=%s", b.setting("restore.receipt_floor"), b.setting("restore.z_floor.eod"), b.setting("restore.jump_token"))
}

func TestBoot_RestoredMainTillJumpsSameBoot(t *testing.T) {
	b := newRestoredBoot(t)
	b.boot(nil, nil)
	if got := b.setting("restore.receipt_floor"); got != "11000" {
		t.Fatalf("receipt floor = %q, want 11000", got)
	}
	if z := b.setting("restore.z_floor.eod"); z == "" || z == "3" {
		t.Fatalf("eod z floor = %q, want above 3", z)
	}
	if b.setting("restore.marker") == "" {
		t.Fatal("restore.marker not set")
	}
	if got := b.setting("restore.jump_token"); got != "restored-install:install-A" {
		t.Fatalf("token = %q", got)
	}
	if b.audits() != 1 {
		t.Fatalf("audit rows = %d, want 1", b.audits())
	}
	if db.NumberJumpPending(b.dbPath) {
		t.Fatal("marker file must be cleared")
	}
	if len(b.log.errors) != 0 {
		t.Fatalf("errors: %v", b.log.errors)
	}
}

func TestBoot_RestoredMainTillJumpsOnlyOnce(t *testing.T) {
	b := newRestoredBoot(t)
	b.boot(nil, nil)
	first := b.floors()
	for i := 0; i < 2; i++ {
		b.boot(nil, nil)
		if got := b.floors(); got != first {
			t.Fatalf("boot %d changed the floors: %s != %s", i+2, got, first)
		}
		if b.audits() != 1 {
			t.Fatalf("boot %d: audit rows = %d, want 1", i+2, b.audits())
		}
		if db.NumberJumpPending(b.dbPath) {
			t.Fatalf("boot %d left a marker file", i+2)
		}
	}
}

func TestBoot_EnvPinnedRestoredMainTillJumpsOnce(t *testing.T) {
	b := newRestoredBoot(t)
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{DeviceID: "pinned"}}
	b.boot(cfg, nil)
	b.boot(cfg, nil)
	if b.audits() != 1 || b.setting("restore.receipt_floor") != "11000" {
		t.Fatalf("audits=%d floors=%s", b.audits(), b.floors())
	}
}

// The jump committed but the re-mark failed (the install id row could not be
// written): the next boot detects again, owes the same token, and the jump is
// recognised as already applied.
type failInstallIDSet struct {
	enroll.Settings
	fail bool
}

func (f *failInstallIDSet) Set(ctx context.Context, key, value string) error {
	if f.fail && key == "till_identity.install_id" {
		return errors.New("simulated crash before the install id row")
	}
	return f.Settings.Set(ctx, key, value)
}

func TestBoot_CrashAfterJumpBeforeRemarkJumpsOnce(t *testing.T) {
	b := newRestoredBoot(t)
	b.boot(nil, &failInstallIDSet{Settings: b.store, fail: true})
	if b.audits() != 1 {
		t.Fatalf("audits after the first boot = %d, want 1", b.audits())
	}
	first := b.floors()
	b.boot(nil, nil)
	if b.audits() != 1 || b.floors() != first {
		t.Fatalf("second boot jumped again: audits=%d %s != %s", b.audits(), b.floors(), first)
	}
	if db.NumberJumpPending(b.dbPath) {
		t.Fatal("marker file must be cleared")
	}
	b.boot(nil, nil)
	if b.audits() != 1 {
		t.Fatalf("third boot audits = %d", b.audits())
	}
}

// A staged Settings -> Backups restore and a detected restore on one boot
// owe one jump, under the staged marker's token.
func TestBoot_StagedAndDetectedRestoreJumpOnce(t *testing.T) {
	b := newRestoredBoot(t)
	staged := time.Now().UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(filepath.Join(filepath.Dir(b.dbPath), "restore-numbers-pending"), []byte(staged+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.boot(nil, nil)
	if b.audits() != 1 {
		t.Fatalf("audits = %d, want 1", b.audits())
	}
	if got := b.setting("restore.jump_token"); got != staged {
		t.Fatalf("token = %q, want the staged %q", got, staged)
	}
	b.boot(nil, nil)
	if b.audits() != 1 || db.NumberJumpPending(b.dbPath) {
		t.Fatalf("second boot: audits=%d pending=%v", b.audits(), db.NumberJumpPending(b.dbPath))
	}
}

func TestBoot_RestoredReplicaOwesNoJump(t *testing.T) {
	b := newRestoredBoot(t)
	b.exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url', 'http://192.168.1.5:8080')`)
	b.boot(nil, nil)
	if db.NumberJumpPending(b.dbPath) {
		t.Fatal("a restored replica owes no marker")
	}
	if b.audits() != 0 || b.setting("restore.jump_token") != "" || b.setting("restore.receipt_floor") != "" {
		t.Fatalf("replica jumped: audits=%d %s", b.audits(), b.floors())
	}
	if p := b.setting("sync.receipt_prefix"); !strings.HasPrefix(p, "R") {
		t.Fatalf("receipt prefix = %q, want provisional R<hex>-", p)
	}
}

func TestBoot_NormalBootOwesNoJump(t *testing.T) {
	b := newRestoredBoot(t)
	if err := os.MkdirAll(paths.Data("secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Data("secrets", "install-id"), []byte("install-A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.boot(nil, nil)
	if b.audits() != 0 || db.NumberJumpPending(b.dbPath) {
		t.Fatalf("normal boot jumped: audits=%d", b.audits())
	}
}

// The jump call follows the restore check in app.Run, and the check can owe.
func TestRunCallsTheNumberJumpAfterRestoreCheck(t *testing.T) {
	raw, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	open := strings.Index(src, "database, attemptErr = db.Open(cfg.DBPath)")
	check := strings.Index(src, "bootRestoreChecks(ctx, cfg, database.DB")
	load := strings.Index(src, "LoadRuntimeConfig(ctx, cfg)")
	if open < 0 || check < open || load < check {
		t.Fatalf("app.Run order: db.Open=%d bootRestoreChecks=%d LoadRuntimeConfig=%d", open, check, load)
	}
	if strings.Contains(src, "jumpNumbersAfterRestore(") {
		t.Fatal("app.Run must reach the jump only through bootRestoreChecks")
	}
	boot, err := os.ReadFile("restore_numbers_boot.go")
	if err != nil {
		t.Fatal(err)
	}
	bs := string(boot)
	c := strings.Index(bs, "enroll.CheckRestoredInstall(")
	j := strings.Index(bs[max(c, 0):], "jumpNumbersAfterRestore(")
	if c < 0 || j < 0 {
		t.Fatal("bootRestoreChecks must call jumpNumbersAfterRestore after enroll.CheckRestoredInstall")
	}
	if !strings.Contains(bs, "db.OweNumberJump(") {
		t.Fatal("the restore check must be given db.OweNumberJump")
	}
}

// A restored folder can carry a marker whose token the original till already
// applied (its clear failed, or a snapshot caught the window). That token is
// spent: the detection replaces it, so the jump happens, exactly once.
func TestBoot_RestoredStaleMarkerOfAppliedTokenStillJumps(t *testing.T) {
	b := newRestoredBoot(t)
	stale := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	b.exec(`INSERT INTO settings (key, value) VALUES ('restore.jump_token', ?)`, stale)
	if err := os.WriteFile(filepath.Join(filepath.Dir(b.dbPath), "restore-numbers-pending"), []byte(stale+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.boot(nil, nil)
	if b.audits() != 1 || b.setting("restore.receipt_floor") != "11000" {
		t.Fatalf("stale marker suppressed the jump: audits=%d %s", b.audits(), b.floors())
	}
	if got := b.setting("restore.jump_token"); got != "restored-install:install-A" {
		t.Fatalf("token = %q, want the detection token", got)
	}
	b.boot(nil, nil)
	if b.audits() != 1 || db.NumberJumpPending(b.dbPath) {
		t.Fatalf("second boot: audits=%d pending=%v", b.audits(), db.NumberJumpPending(b.dbPath))
	}
}
