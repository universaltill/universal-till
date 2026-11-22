package app

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#6907: a data folder copied (secrets/ included) onto other hardware
// owes the number jump once. Hosts are simulated with cfg.HostID (UT_HOST_ID).

type hostFolder struct {
	t      *testing.T
	dir    string
	dbPath string
	d      *db.DB
	store  *settings.Store
}

func openFolder(t *testing.T, dir string) *hostFolder {
	t.Helper()
	paths.Init(dir)
	dbPath := filepath.Join(dir, "unitill-pos.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	f := &hostFolder{t: t, dir: dir, dbPath: dbPath, d: d, store: settings.NewStore(d.DB)}
	t.Cleanup(func() { _ = d.Close() })
	return f
}

func (f *hostFolder) boot(host string) {
	f.t.Helper()
	paths.Init(f.dir)
	cfg := &config.Config{DBPath: f.dbPath, HostID: host}
	bootRestoreChecks(context.Background(), cfg, f.d.DB, f.store, time.Now(), &recordingLog{})
}

func (f *hostFolder) setting(k string) string {
	f.t.Helper()
	var v string
	if err := f.d.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, k).Scan(&v); err != nil && !errors.Is(err, sql.ErrNoRows) {
		f.t.Fatal(err)
	}
	return v
}

func (f *hostFolder) audits() int {
	f.t.Helper()
	var n int
	if err := f.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'restore_numbers_jumped'`).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *hostFolder) dump() string {
	f.t.Helper()
	var sb strings.Builder
	for _, q := range []string{`SELECT key || '=' || value FROM settings`, `SELECT COALESCE(entity_id,'') || COALESCE(data_json, '') FROM audit_log`} {
		rows, err := f.d.DB.Query(q)
		if err != nil {
			f.t.Fatal(err)
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			sb.WriteString(s + "\n")
		}
		_ = rows.Close()
	}
	return sb.String()
}

// copyFolder is `cp -a A B` for a closed folder: every file, secrets/ too.
func copyFolder(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// settledMain is a main till that sold up to receipt 42 / Z 3 and booted on host-1.
func settledMain(t *testing.T) *hostFolder {
	t.Helper()
	orig := paths.DataDir()
	t.Cleanup(func() { paths.Init(orig) })
	a := openFolder(t, t.TempDir())
	now := time.Now().UTC()
	for _, q := range []struct {
		q string
		a []any
	}{
		{`INSERT INTO sales (id, receipt_no, subtotal, total, created_at) VALUES ('s42', '000000042', 100, 100, ?)`, []any{now.AddDate(0, 0, -2).Format(time.RFC3339)}},
		{`INSERT INTO report_archive (id, kind, period, content_json, z_number, created_at) VALUES ('ra3', 'eod', 'p3', '{}', 3, ?)`, []any{now.AddDate(0, 0, -2).Format("2006-01-02T15:04:05.000Z")}},
	} {
		if _, err := a.d.DB.Exec(q.q, q.a...); err != nil {
			t.Fatal(err)
		}
	}
	a.boot("host-1")
	if a.audits() != 0 {
		t.Fatal("the first boot must adopt, not jump")
	}
	return a
}

// copyTo closes nothing: sqlite WAL is checkpointed by a copy of db + wal files.
func (a *hostFolder) copyTo(t *testing.T) *hostFolder {
	t.Helper()
	_, _ = a.d.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	dir := t.TempDir()
	copyFolder(t, a.dir, dir)
	return openFolder(t, dir)
}

func TestBoot_CopiedFolderOnNewHostJumpsOnce(t *testing.T) {
	a := settledMain(t)
	b := a.copyTo(t)
	b.boot("host-2")
	if b.audits() != 1 {
		t.Fatalf("copy audits = %d, want 1", b.audits())
	}
	if got := b.setting("restore.receipt_floor"); got != "11000" {
		t.Fatalf("receipt floor = %q, want 11000", got)
	}
	if z := b.setting("restore.z_floor.eod"); z == "" || z == "3" {
		t.Fatalf("z floor = %q, want above 3", z)
	}
	if !strings.HasPrefix(b.setting("restore.jump_token"), "copied-host:") {
		t.Fatalf("token = %q", b.setting("restore.jump_token"))
	}
	// F2 (#6907): the host move leaves the install id alone, so the next
	// boot's #5157 check owes nothing and wipes nothing.
	ma, _ := os.ReadFile(filepath.Join(a.dir, "secrets", "install-id"))
	mb, _ := os.ReadFile(filepath.Join(b.dir, "secrets", "install-id"))
	if len(ma) == 0 || string(ma) != string(mb) || a.setting("till_identity.install_id") != b.setting("till_identity.install_id") {
		t.Fatalf("install id changed by the host move: %q -> %q", ma, mb)
	}
	tokenBefore := b.setting("restore.jump_token")
	b.boot("host-2")
	if b.audits() != 1 || b.setting("restore.jump_token") != tokenBefore || b.setting("till_identity.restored_at") != "" {
		t.Fatalf("rebooted copy: audits=%d token %q (was %q) restored_at %q", b.audits(), b.setting("restore.jump_token"), tokenBefore, b.setting("till_identity.restored_at"))
	}
	// The original is untouched.
	a.boot("host-1")
	if a.audits() != 0 || a.setting("restore.receipt_floor") != "" {
		t.Fatalf("original jumped: audits=%d floor=%q", a.audits(), a.setting("restore.receipt_floor"))
	}
	if strings.Contains(b.dump(), "host-2") || strings.Contains(b.dump(), "host-1") {
		t.Fatalf("raw host id stored:\n%s", b.dump())
	}
}

func TestBoot_CopyWithMissingMarkerJumpsOnce(t *testing.T) {
	a := settledMain(t)
	b := a.copyTo(t)
	if err := os.Remove(filepath.Join(b.dir, "secrets", "install-id")); err != nil {
		t.Fatal(err)
	}
	b.boot("host-2")
	if b.audits() != 1 {
		t.Fatalf("both detectors fired: audits = %d, want 1", b.audits())
	}
	requireHostCheckRan(t, a, b)
	b.boot("host-2")
	if b.audits() != 1 {
		t.Fatalf("second boot audits = %d", b.audits())
	}
}

func TestBoot_StagedRestoreAndHostChangeJumpOnce(t *testing.T) {
	a := settledMain(t)
	b := a.copyTo(t)
	if err := db.OweNumberJump(b.dbPath, "staged-x", nil); err != nil {
		t.Fatal(err)
	}
	b.boot("host-2")
	if b.audits() != 1 {
		t.Fatalf("audits = %d, want 1", b.audits())
	}
	if got := b.setting("restore.jump_token"); got != "staged-x" {
		t.Fatalf("token = %q, want the staged one", got)
	}
	requireHostCheckRan(t, a, b)
}

// requireHostCheckRan keeps the stacking tests from passing with the host
// check dead: the copy re-recorded its own tag, which differs from the
// original's.
func requireHostCheckRan(t *testing.T, a, b *hostFolder) {
	t.Helper()
	ta, tb := a.setting("till_identity.host_tag"), b.setting("till_identity.host_tag")
	if !strings.HasPrefix(ta, "pinned:") || !strings.HasPrefix(tb, "pinned:") || ta == tb {
		t.Fatalf("host tag not re-recorded on the copy: original %q copy %q", ta, tb)
	}
}
