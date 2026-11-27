package registeraudit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

const (
	secretBearer = "BEARERHASH-do-not-print-0123456789"
	secretKey    = "RAW-APPROVAL-KEY-do-not-print-abcdef"
)

// ownerFixture is the owner's shape (ut-docs#7098): the Mac's own register,
// phone-till2 live, and two ghosts: "phone till" (replaced by phone-till2's
// device) and "iphone-till" (its till row is gone). Returns the DB path.
func ownerFixture(t *testing.T) string {
	t.Helper()
	path := testsupport.MigratedDBFile(t, "audit.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	for _, s := range []string{
		`INSERT INTO registers (id, name, is_active) VALUES ('reg-default','mac-till',1),('r-live','phone-till2',1),('r-g1','phone till',1),('r-g2','iphone-till',1)`,
		`INSERT INTO settings (key, value) VALUES ('sync.till_register_id','reg-default')`,
		`INSERT INTO tills (id, name, bearer_hash, device_id, enrolled_at, last_seen_at) VALUES ('tB','phone-till2','` + secretBearer + `','dev-B','` + old + `','` + recent + `')`,
		`INSERT INTO register_devices (register_id, device_id, till_id, source, superseded_by) VALUES
		   ('r-live','dev-B','tB','enrol',''),
		   ('r-g1','dev-A','tA','enrol','dev-B'),
		   ('r-g2','dev-C','tC','enrol','')`,
		`INSERT INTO sales (id, receipt_no, subtotal, total, register_id, sync_status, created_at) VALUES
		   ('s1','R1',0,0,'r-g1','synced','` + old + `'),
		   ('s2','R2',0,0,'r-g2','synced','` + old + `'),
		   ('s3','R3',0,0,'r-live','synced','` + recent + `')`,
	} {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed %.50s: %v", s, err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func hashFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// sideFiles describes the -wal/-shm files next to path (name and size).
func sideFiles(path string) string {
	var out []string
	for _, side := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		if fi, err := os.Stat(side); err == nil {
			out = append(out, fmt.Sprintf("%s:%d", filepath.Base(side), fi.Size()))
		}
	}
	return strings.Join(out, ",")
}

func buildReport(t *testing.T, path string, cloud []CloudDevice) Report {
	t.Helper()
	d, err := db.OpenAuditReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rep, err := Build(context.Background(), d.DB, Options{Now: time.Now(), Cloud: cloud, Build: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// Owner shape: exactly 2 ghost_candidate rows, both retire_safe, one self, one live.
func TestAuditOwnerShape(t *testing.T) {
	rep := buildReport(t, ownerFixture(t), nil)
	if rep.Counts["ghost_candidate"] != 2 || rep.Counts["self"] != 1 || rep.Counts["live"] != 1 || len(rep.Rows) != 4 {
		t.Fatalf("counts = %v rows=%d, want ghost_candidate:2 self:1 live:1 of 4", rep.Counts, len(rep.Rows))
	}
	for _, r := range rep.Rows {
		switch r.RegisterID {
		case "r-g1", "r-g2":
			if r.State != "ghost_candidate" || !r.RetireSafe {
				t.Fatalf("%s: state=%q retire_safe=%v, want ghost_candidate true (%s)", r.Name, r.State, r.RetireSafe, r.Evidence)
			}
		case "reg-default":
			if r.State != "self" || r.RetireSafe {
				t.Fatalf("self row: %+v", r)
			}
		case "r-live":
			if r.State != "live" || r.RetireSafe {
				t.Fatalf("live row: %+v", r)
			}
		}
	}
	for _, r := range rep.Rows {
		if r.RegisterID == "r-g1" && r.SupersededBy != "dev-B" {
			t.Fatalf("phone till superseded_by = %q, want dev-B", r.SupersededBy)
		}
	}
}

// A ghost with an unsent sale is never retire_safe and the evidence says why.
func TestAuditUnsentSalesBlockRetireSafe(t *testing.T) {
	path := ownerFixture(t)
	w, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.DB.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total, register_id, sync_status) VALUES ('q','Rq',0,0,'r-g2','queued')`); err != nil {
		t.Fatal(err)
	}
	w.Close()
	rep := buildReport(t, path, nil)
	for _, r := range rep.Rows {
		if r.RegisterID == "r-g2" && (r.RetireSafe || !strings.Contains(r.Evidence, "unsent sales")) {
			t.Fatalf("r-g2 retire_safe=%v evidence=%q, want false naming unsent sales", r.RetireSafe, r.Evidence)
		}
	}
}

// Read-only by construction: the file hash is identical before and after, and
// a write through the same connection is refused by SQLite.
func TestAuditIsReadOnly(t *testing.T) {
	path := ownerFixture(t)
	before := hashFile(t, path)
	sideBefore := sideFiles(path)
	rep := buildReport(t, path, nil)
	var buf bytes.Buffer
	if err := WriteTable(&buf, rep); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	if after := hashFile(t, path); after != before {
		t.Fatalf("the audit changed the database file: %s -> %s", before, after)
	}
	// SQLite itself may create an EMPTY -wal and a -shm index to read a WAL
	// database (housekeeping, no data); a non-empty -wal would mean a write.
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() != 0 {
		t.Fatalf("side files %q -> %q: the audit left frames in the -wal file", sideBefore, sideFiles(path))
	}
	d, err := db.OpenAuditReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{
		`INSERT INTO registers (id, name, is_active) VALUES ('x','x',1)`,
		`UPDATE registers SET is_active = 0`,
		`DELETE FROM registers`,
		`CREATE TABLE zz (a)`,
	} {
		if _, err := d.DB.Exec(q); err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
			t.Fatalf("%q: err = %v, want an attempt to write a readonly database", q, err)
		}
	}
	if after := hashFile(t, path); after != before {
		t.Fatal("a refused write still changed the file")
	}
}

// Secrets never reach the output: no bearer hash, no raw key; the key
// fingerprint is 8 hex.
func TestAuditNeverPrintsSecrets(t *testing.T) {
	path := ownerFixture(t)
	cloud := []CloudDevice{
		{DeviceID: "dev-B", TillID: "tB", Role: "replica", KeyFingerprint: secretKey},
		{DeviceID: "dev-C", TillID: "tC", Role: "replica", RevokedReason: "removed", KeyFingerprint: "deadbeef"},
	}
	rep := buildReport(t, path, cloud)
	var buf bytes.Buffer
	_ = WriteTable(&buf, rep)
	_ = WriteJSON(&buf, rep)
	out := buf.String()
	for _, secret := range []string{secretBearer, secretKey} {
		if strings.Contains(out, secret) {
			t.Fatalf("output contains %q", secret)
		}
	}
	for _, r := range rep.Rows {
		if r.RegisterID == "r-live" {
			if len(r.KeyFingerprint) != 8 {
				t.Fatalf("key fingerprint %q, want 8 hex", r.KeyFingerprint)
			}
			if _, err := hex.DecodeString(r.KeyFingerprint); err != nil {
				t.Fatalf("key fingerprint %q is not hex", r.KeyFingerprint)
			}
			if r.InCloud != "yes" {
				t.Fatalf("in_cloud = %q, want yes", r.InCloud)
			}
		}
		if r.RegisterID == "r-g2" && r.CloudState != "revoked: removed" {
			t.Fatalf("r-g2 cloud_state = %q, want revoked: removed", r.CloudState)
		}
	}
}

// A database from before migration 130 still audits: no link table, the
// enrolment audit row is the link, state unknown, no crash.
func TestAuditOnOlderSchema(t *testing.T) {
	path := testsupport.MigratedDBFile(t, "old.db")
	w, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`DROP TABLE register_devices`,
		`INSERT INTO registers (id, name, is_active) VALUES ('reg-default','Main',1),('r1','phone',1)`,
	} {
		if _, err := w.DB.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	w.Close()
	rep := buildReport(t, path, nil)
	if len(rep.Rows) != 2 || !strings.HasPrefix(rep.Comparison.LinkTableSchema, "absent") {
		t.Fatalf("rows=%d link=%q", len(rep.Rows), rep.Comparison.LinkTableSchema)
	}
	for _, r := range rep.Rows {
		if r.RegisterID == "r1" && r.State != StateUnknown {
			t.Fatalf("r1 state = %q, want unknown", r.State)
		}
	}
}

var _ = sql.ErrNoRows

// Empty comparison lists are JSON [] (not null) and the main till's own cloud
// row is not listed as a device without a register.
func TestAuditEmptyListsAreArraysAndMainTillNotAGap(t *testing.T) {
	cloud := []CloudDevice{{DeviceID: "dev-main-self", TillID: "t-main-self", Role: "main"}}
	rep := buildReport(t, ownerFixture(t), cloud)
	b, err := json.Marshal(rep.Comparison)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "null") {
		t.Fatalf("comparison JSON has null: %s", b)
	}
	for _, id := range rep.Comparison.CloudWithoutRegister {
		if id == "t-main-self" {
			t.Fatalf("main till listed as cloud device without a register: %v", rep.Comparison.CloudWithoutRegister)
		}
	}
}
