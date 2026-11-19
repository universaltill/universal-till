package offsitebackup

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

type tillEnv struct {
	t      *testing.T
	dbPath string
	dir    string
	conn   *sql.DB
	set    *data.SettingsRepo
	cloud  *fakeCloud
	up     *Uploader
	now    time.Time
	gate   GateState
}

func newTill(t *testing.T) *tillEnv {
	t.Helper()
	p := filepath.Join(t.TempDir(), "unitill-pos.db")
	d, err := db.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	e := &tillEnv{t: t, dbPath: p, conn: d.DB, set: data.NewSettingsRepo(d.DB), cloud: newFakeCloud(t),
		now: time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC), gate: GateOpen}
	e.dir, _ = db.BackupDir(p)
	for k, v := range map[string]string{
		"marketplace.store_id": "st_1", "marketplace.token": "SECRET-TOKEN", "marketplace.device_id": "dev-1",
		"store.name": "Test Bakery", data.OffsiteKeyInCloudKey: data.OffsiteKeyInCloudOn,
	} {
		if err := e.set.Set(context.Background(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	e.up = &Uploader{DBPath: p, Settings: e.set, Cloud: e.cloud.cloud(), AppVersion: "0.37.0",
		SchemaVersion: func() int { return 212 }, Now: func() time.Time { return e.now },
		Sleep:    func(context.Context, time.Duration) {},
		FreeDisk: func(string) (uint64, error) { return 1 << 40, nil },
		Gate:     func(context.Context, time.Time) GateState { return e.gate },
		Keys:     KeyStore{Dir: filepath.Join(t.TempDir(), "keys")}, StoreID: func() string { return "st_1" }}
	return e
}

func (e *tillEnv) snapshot() string {
	e.t.Helper()
	p, err := db.Snapshot(e.conn, e.dbPath)
	if err != nil {
		e.t.Fatal(err)
	}
	// The files are made at real time but the uploader's clock is e.now:
	// age them against the test clock (ADR/ut-docs#5523 effective time).
	if err := os.Chtimes(p, e.now, e.now); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// snapshotCopy adds a snapshot with a given name and mtime by copying the
// newest one (db.Snapshot names files by the second, so two calls in one
// second share a file).
func (e *tillEnv) snapshotCopy(name string, mtime time.Time) {
	e.t.Helper()
	list, err := db.ListBackups(e.dbPath)
	if err != nil || len(list) == 0 {
		e.t.Fatal("no snapshot to copy", err)
	}
	raw, err := os.ReadFile(filepath.Join(e.dir, list[0].Name))
	if err != nil {
		e.t.Fatal(err)
	}
	p := filepath.Join(e.dir, name)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		e.t.Fatal(err)
	}
}

func (e *tillEnv) get(k string) string {
	v, _, _ := e.set.Get(context.Background(), k)
	return v
}

func (e *tillEnv) round(force bool) {
	e.t.Helper()
	if err := e.up.Round(context.Background(), force); err != nil {
		e.t.Fatal(err)
	}
}

func (e *tillEnv) onlyBackup() *fakeBackup {
	e.t.Helper()
	if len(e.cloud.backups) != 1 {
		e.t.Fatalf("cloud holds %d backups, want 1", len(e.cloud.backups))
	}
	for _, b := range e.cloud.backups {
		return b
	}
	return nil
}

func (e *tillEnv) noTempFiles() {
	e.t.Helper()
	es, _ := os.ReadDir(e.dir)
	for _, f := range es {
		if strings.HasPrefix(f.Name(), "offsite-") {
			e.t.Errorf("temp file left behind: %s", f.Name())
		}
	}
}

func TestUploadHappyPathStripsIdentityAndSetsKeys(t *testing.T) {
	e := newTill(t)
	snap := e.snapshot()
	e.round(false)
	b := e.onlyBackup()
	if !b.complete {
		t.Fatal("backup not completed")
	}
	if b.begin.SchemaVersion != 212 || b.begin.AppVersion != "0.37.0" || b.begin.Format != 1 || b.begin.TakenAt == "" {
		t.Errorf("begin body: %+v", b.begin)
	}
	// AC1.6: decrypt what the cloud holds and look for the identity.
	gz, err := e.cloud.decrypt(b.id)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	out := filepath.Join(t.TempDir(), "restored.db")
	if err := os.WriteFile(out, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	d2, err := db.OpenReadOnly(out)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	var n int
	if err := d2.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'marketplace.token%' OR key LIKE 'marketplace.device_%' OR key LIKE 'till_identity.%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("identity rows in the uploaded copy: %d %v", n, err)
	}
	var name string
	if err := d2.QueryRow(`SELECT value FROM settings WHERE key = 'store.name'`).Scan(&name); err != nil || name != "Test Bakery" {
		t.Fatalf("data missing from the copy: %q %v", name, err)
	}
	// The live snapshot is untouched (still has the identity).
	sd, _ := db.OpenReadOnly(snap)
	defer sd.Close()
	_ = sd.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'marketplace.token'`).Scan(&n)
	if n != 1 {
		t.Errorf("the snapshot itself was altered")
	}
	// AC1.9
	if e.get(data.OffsiteLastSuccessAtKey) == "" || e.get(data.BackupCopiedAtSettingsKey) == "" ||
		e.get(data.OffsiteLastSnapshotKey) != filepath.Base(snap) {
		t.Errorf("success keys not set")
	}
	if e.get(data.OffsitePendingIDKey) != "" || e.get(data.OffsiteFailingSinceKey) != "" {
		t.Errorf("pending/failing state not cleared")
	}
	e.noTempFiles()
	// Nothing in the settings or the temp dir holds the key.
	all, _ := e.set.All(context.Background())
	dek, _, _ := e.up.Keys.Load("st_1")
	for k, v := range all {
		if strings.Contains(v, base64.StdEncoding.EncodeToString(dek)) || strings.Contains(strings.ToLower(k), "key_b64") {
			t.Errorf("key material in settings %s", k)
		}
	}
}

func TestUploadHonoursTwentyHourRuleAndSameSnapshot(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.round(false)
	e.now = e.now.Add(2 * time.Hour)
	e.snapshotCopy("unitill-pos-20991231-050000.db", e.now)
	e.round(false) // success 2 h ago: nothing
	if e.cloud.begins != 1 {
		t.Fatalf("begins = %d, want 1", e.cloud.begins)
	}
	e.now = e.now.Add(21 * time.Hour)
	e.round(false) // 23 h later, but no snapshot newer than the one uploaded? the newest snapshot is the second.
	if e.cloud.begins != 2 {
		t.Fatalf("begins = %d, want 2 (newer snapshot, 20 h passed)", e.cloud.begins)
	}
	e.now = e.now.Add(21 * time.Hour)
	e.round(false) // same snapshot again: nothing
	if e.cloud.begins != 2 {
		t.Fatalf("re-uploaded the same snapshot: begins = %d", e.cloud.begins)
	}
	e.round(true) // "Upload now" does not re-send what the cloud already holds
	if e.cloud.begins != 2 {
		t.Fatalf("forced round re-uploaded the same snapshot: begins = %d", e.cloud.begins)
	}
}

func TestUploadSendsNameStampWhenMtimeIsInTheFuture(t *testing.T) {
	e := newTill(t)
	stamp := e.now.Add(-48 * time.Hour)
	p := e.snapshot()
	named := filepath.Join(e.dir, "unitill-pos-"+stamp.Format("20060102-150405")+".db")
	if err := os.Rename(p, named); err != nil {
		t.Fatal(err)
	}
	future := e.now.Add(72 * time.Hour)
	if err := os.Chtimes(named, future, future); err != nil {
		t.Fatal(err)
	}
	e.round(false)
	if got, want := e.onlyBackup().begin.TakenAt, stamp.UTC().Format(time.RFC3339); got != want {
		t.Fatalf("TakenAt = %s, want the name stamp %s", got, want)
	}
}

func TestUploadSkipsOwnSnapshotOfUnknownAge(t *testing.T) {
	e := newTill(t)
	p := e.snapshot()
	named := filepath.Join(e.dir, "unitill-pos-20991231-050000.db")
	if err := os.Rename(p, named); err != nil {
		t.Fatal(err)
	}
	future := e.now.Add(72 * time.Hour)
	if err := os.Chtimes(named, future, future); err != nil {
		t.Fatal(err)
	}
	e.round(false)
	if e.cloud.begins != 0 {
		t.Fatalf("begins = %d, want 0 for an unknown-age snapshot", e.cloud.begins)
	}
}

func TestGatesNeverCallBegin(t *testing.T) {
	for name, g := range map[string]GateState{"additional till": GateNotMain, "unpaired": GateUnpaired, "unentitled": GateNotEntitled} {
		e := newTill(t)
		e.snapshot()
		e.gate = g
		e.round(true)
		if e.cloud.begins != 0 {
			t.Errorf("%s: begin was called", name)
		}
	}
}

func TestNoSnapshotIsQuiet(t *testing.T) {
	e := newTill(t)
	e.round(false)
	if e.cloud.begins != 0 || e.get(data.OffsiteFailingSinceKey) != "" {
		t.Fatalf("begins=%d failing=%q", e.cloud.begins, e.get(data.OffsiteFailingSinceKey))
	}
}

func TestUploadedBackupFileIsNotUploaded(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	// An upload a manager made (IsUploadedBackupName) sorts newest but is not this till's data.
	if err := os.WriteFile(filepath.Join(e.dir, "unitill-pos-20991231-000000-file.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.round(false)
	b := e.onlyBackup()
	if b.begin.SnapshotBytes <= 1 {
		t.Fatalf("uploaded the manager's file instead of the snapshot")
	}
}

func TestFiveXXBacksOffThenSucceeds(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.cloud.beginStatus, e.cloud.beginCode = 500, "internal"
	e.round(false)
	if e.get(data.OffsiteLastErrorKey) != ReasonServer || e.get(data.OffsiteAttemptsKey) != "1" || e.get(data.OffsiteFailingSinceKey) == "" {
		t.Fatalf("failure not recorded: %q %q", e.get(data.OffsiteLastErrorKey), e.get(data.OffsiteAttemptsKey))
	}
	next, _ := time.Parse(time.RFC3339, e.get(data.OffsiteNextAttemptAtKey))
	if next.Sub(e.now) != 15*time.Minute {
		t.Fatalf("first backoff = %s", next.Sub(e.now))
	}
	// Inside the backoff: no call.
	b0 := e.cloud.begins
	e.now = e.now.Add(5 * time.Minute)
	e.round(false)
	if e.cloud.begins != b0 {
		t.Fatal("called inside the backoff window")
	}
	// Past it: fails again, 30 minutes.
	e.now = e.now.Add(11 * time.Minute)
	e.round(false)
	next, _ = time.Parse(time.RFC3339, e.get(data.OffsiteNextAttemptAtKey))
	if next.Sub(e.now) != 30*time.Minute || e.get(data.OffsiteAttemptsKey) != "2" {
		t.Fatalf("second backoff = %s attempts %s", next.Sub(e.now), e.get(data.OffsiteAttemptsKey))
	}
	// Recovery resets everything.
	e.cloud.beginStatus = 0
	e.now = e.now.Add(time.Hour)
	e.round(false)
	if e.get(data.OffsiteAttemptsKey) != "" || e.get(data.OffsiteFailingSinceKey) != "" || e.get(data.OffsiteLastSuccessAtKey) == "" {
		t.Fatal("success did not reset the failure state")
	}
}

func TestBackoffCapsAtSixHours(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.cloud.beginStatus = 500
	for i := 0; i < 9; i++ {
		e.now = e.now.Add(7 * time.Hour)
		e.round(false)
	}
	next, _ := time.Parse(time.RFC3339, e.get(data.OffsiteNextAttemptAtKey))
	if next.Sub(e.now) != 6*time.Hour {
		t.Fatalf("cap = %s", next.Sub(e.now))
	}
}

func TestPaymentRequiredIsARefusalNotAFailure(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.cloud.beginStatus, e.cloud.beginCode = 402, "cloud_backup_not_in_plan"
	e.round(false)
	if e.get(data.OffsiteRefusedReasonKey) != "cloud_backup_not_in_plan" {
		t.Fatalf("refused reason = %q", e.get(data.OffsiteRefusedReasonKey))
	}
	if e.get(data.OffsiteFailingSinceKey) != "" || e.get(data.OffsiteLastErrorKey) != "" {
		t.Fatal("a 402 must not show as a failure")
	}
	next, _ := time.Parse(time.RFC3339, e.get(data.OffsiteNextAttemptAtKey))
	if next.Sub(e.now) != 24*time.Hour {
		t.Fatalf("402 wait = %s", next.Sub(e.now))
	}
	b := e.cloud.begins
	e.now = e.now.Add(time.Hour)
	e.round(false)
	if e.cloud.begins != b {
		t.Fatal("retried inside the 24 h stop")
	}
	// A plan change clears it: success removes the refusal.
	e.cloud.beginStatus = 0
	e.round(true)
	if e.get(data.OffsiteRefusedReasonKey) != "" {
		t.Fatal("refusal kept after success")
	}
}

func TestDiskShortSkipsWithReasonAndNoCopy(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.up.FreeDisk = func(string) (uint64, error) { return 10, nil }
	e.round(false)
	if e.cloud.begins != 0 || e.get(data.OffsiteLastErrorKey) != ReasonDisk {
		t.Fatalf("begins=%d err=%q", e.cloud.begins, e.get(data.OffsiteLastErrorKey))
	}
	e.noTempFiles()
}

func TestResumeAfterRestartSendsOnlyMissingParts(t *testing.T) {
	e := newTill(t)
	e.cloud.partMax = 64 << 10 // many small parts
	// A snapshot with some bulk so there are several parts.
	if _, err := e.conn.Exec(`CREATE TABLE IF NOT EXISTS bulk_t (b BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.conn.Exec(`INSERT INTO bulk_t VALUES (randomblob(400000))`); err != nil {
		t.Fatal(err)
	}
	e.snapshot()
	// Part 3 fails for good (500 x3 retries), earlier parts are stored.
	e.cloud.putFailures = 0
	failAt := int32(0)
	orig := e.up.Cloud
	e.up.Cloud = &cutCloud{Cloud: orig, failFrom: 3, calls: &failAt}
	e.round(false)
	b := e.onlyBackup()
	if b.complete || len(b.parts) != 2 {
		t.Fatalf("complete=%v parts=%d, want 2 stored", b.complete, len(b.parts))
	}
	pid := e.get(data.OffsitePendingIDKey)
	if pid != b.id || e.get(data.OffsitePendingFileKey) == "" {
		t.Fatalf("pending state not kept: %q", pid)
	}
	// "Restart": a fresh uploader, healthy cloud. Same backup id, only the rest is PUT.
	e.up.Cloud = orig
	e.cloud.putCalls = 0
	e.now = e.now.Add(time.Hour)
	e.round(false)
	if !b.complete || len(e.cloud.backups) != 1 {
		t.Fatalf("not resumed: complete=%v backups=%d", b.complete, len(e.cloud.backups))
	}
	if e.cloud.begins != 1 {
		t.Fatalf("resume called begin again (%d)", e.cloud.begins)
	}
	want := len(b.parts) - 2
	if e.cloud.putCalls != want {
		t.Fatalf("put %d parts on resume, want only the %d missing", e.cloud.putCalls, want)
	}
	if _, err := e.cloud.decrypt(b.id); err != nil {
		t.Fatal(err)
	}
	e.noTempFiles()
}

// cutCloud fails every PUT from part failFrom on with a network-ish 500.
type cutCloud struct {
	Cloud
	failFrom int
	calls    *int32
}

func (c *cutCloud) PutPart(ctx context.Context, id string, n int, body []byte, sha string) error {
	if n >= c.failFrom {
		atomic.AddInt32(c.calls, 1)
		return &APIError{Status: 500, Code: "internal"}
	}
	return c.Cloud.PutPart(ctx, id, n, body, sha)
}

func TestResumeDropsBackupTheCloudForgot(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	failAt := int32(0)
	orig := e.up.Cloud
	e.up.Cloud = &cutCloud{Cloud: orig, failFrom: 1, calls: &failAt}
	e.round(false)
	if e.get(data.OffsitePendingIDKey) == "" {
		t.Fatal("no pending state")
	}
	// The cloud dropped it (24 h abandon sweep).
	for id := range e.cloud.backups {
		delete(e.cloud.backups, id)
	}
	e.up.Cloud = orig
	e.now = e.now.Add(time.Hour)
	e.round(false)
	if len(e.cloud.backups) != 1 || !e.onlyBackup().complete {
		t.Fatalf("did not start over after the cloud forgot the backup")
	}
	e.noTempFiles()
}

func TestPartRetriesThenSucceeds(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.cloud.putFailures = 2 // under the 3 attempts
	e.round(false)
	if !e.onlyBackup().complete || e.get(data.OffsiteFailingSinceKey) != "" {
		t.Fatal("two transient part failures should be absorbed")
	}
}

func TestProblemAfter48HoursOfFailure(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.cloud.beginStatus = 500
	e.round(false)
	since := e.get(data.OffsiteFailingSinceKey)
	e.now = e.now.Add(49 * time.Hour)
	e.round(false)
	if e.get(data.OffsiteFailingSinceKey) != since {
		t.Fatal("failing_since moved while still failing")
	}
}

// AC1.4: a 50 MB snapshot is sealed and sent with bounded memory.
func TestUploadMemoryIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("memory bound")
	}
	e := newTill(t)
	snapDir := e.dir
	// A plain 50 MB file named like a snapshot; the copy step is a plain copy
	// here so the test measures the seal+upload path, not SQLite's page cache.
	name := "unitill-pos-20261116-020000.db"
	f, err := os.Create(filepath.Join(snapDir, name))
	if err != nil {
		t.Fatal(err)
	}
	chunk := randBytes(t, 1<<20)
	for i := 0; i < 50; i++ {
		_, _ = f.Write(chunk)
	}
	f.Close()
	e.up.Strip = func(src, dst string) error {
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	}
	// The fake cloud keeps parts in memory (it is the test's own state), so
	// swap it for a sink that hashes and drops.
	sink := &sinkCloud{Cloud: e.up.Cloud}
	e.up.Cloud = sink

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.Load() {
					peak.Store(m.HeapAlloc)
				}
			}
		}
	}()
	e.round(false)
	close(stop)
	<-done
	if sink.parts < 6 || !sink.completed {
		t.Fatalf("parts=%d completed=%v", sink.parts, sink.completed)
	}
	growth := int64(peak.Load()) - int64(base.HeapAlloc)
	t.Logf("heap growth over a 50 MB snapshot: %d KiB", growth>>10)
	if growth > 16<<20 {
		t.Fatalf("heap grew %d MiB for a 50 MB snapshot (limit 16)", growth>>20)
	}
}

// sinkCloud answers begin like the fake but drops part bodies.
type sinkCloud struct {
	Cloud
	parts     int
	completed bool
}

func (s *sinkCloud) PutPart(ctx context.Context, id string, n int, body []byte, sha string) error {
	s.parts++
	return nil
}
func (s *sinkCloud) Complete(ctx context.Context, id string, r CompleteRequest) error {
	s.completed = true
	return nil
}

func TestSettingsNeverHoldTheKeyOrLogIt(t *testing.T) {
	// AC2.4: a grep guard over the package's non-test source.
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		for i, line := range strings.Split(string(src), "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "//") {
				continue
			}
			if (strings.Contains(l, "KeyB64") || strings.Contains(l, "key_b64")) &&
				(strings.Contains(l, "Set(") || strings.Contains(l, "SetMany") || strings.Contains(l, "Printf") ||
					strings.Contains(l, "Infof") || strings.Contains(l, "Warnf") || strings.Contains(l, "Errorf")) {
				t.Errorf("%s:%d writes or logs key material: %s", f, i+1, l)
			}
		}
	}
}

func TestConcurrentRoundIsSingleFlight(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.up.mu.Lock() // a round is "running"
	e.round(false) // must return at once, not block or start a second
	e.up.mu.Unlock()
	if e.cloud.begins != 0 {
		t.Fatal("second round ran")
	}
}
