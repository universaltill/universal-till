package pos

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/universaltill/universal-till/internal/settings"
)

// testRegisterIdentityDB is a minimal schema for register-identity
// resolution: registers plus the generic settings key/value table the
// identity persists into (column-identical to 001_init.sql, same drift
// rule as the other test fixtures).
func testRegisterIdentityDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE registers (id TEXT PRIMARY KEY, name TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT);`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return db
}

func persistedRegisterID(t *testing.T, st *settings.Store) (string, bool) {
	t.Helper()
	v, ok, err := st.Get(context.Background(), SettingsKeyTillRegisterID)
	if err != nil {
		t.Fatalf("get persisted register id: %v", err)
	}
	return v, ok
}

// A persisted, still-active register IS the till's identity — returned
// as-is even when other registers exist (this is precisely the
// two-register case ut-docs#268 is about).
func TestResolveTillRegisterID_PersistedAndValid(t *testing.T) {
	ctx := context.Background()
	db := testRegisterIdentityDB(t)
	st := settings.NewStore(db)
	for _, ins := range []string{
		`INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`,
		`INSERT INTO registers(id,name,is_active) VALUES('reg2','Back Till',1)`,
	} {
		if _, err := db.Exec(ins); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Set(ctx, SettingsKeyTillRegisterID, "reg2"); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveTillRegisterID(ctx, db, st)
	if err != nil {
		t.Fatalf("ResolveTillRegisterID: %v", err)
	}
	if got != "reg2" {
		t.Fatalf("expected the persisted identity reg2, got %q", got)
	}
}

// A persisted id whose register has since been deactivated is stale — it
// must fall through to re-resolution, and with exactly one remaining
// active register that one is unambiguous: adopted and re-persisted.
func TestResolveTillRegisterID_PersistedButStaleReResolves(t *testing.T) {
	ctx := context.Background()
	db := testRegisterIdentityDB(t)
	st := settings.NewStore(db)
	for _, ins := range []string{
		`INSERT INTO registers(id,name,is_active) VALUES('reg-old','Retired Till',0)`,
		`INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`,
	} {
		if _, err := db.Exec(ins); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Set(ctx, SettingsKeyTillRegisterID, "reg-old"); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveTillRegisterID(ctx, db, st)
	if err != nil {
		t.Fatalf("ResolveTillRegisterID: %v", err)
	}
	if got != "reg1" {
		t.Fatalf("expected re-resolution to the sole active register reg1, got %q", got)
	}
	if v, ok := persistedRegisterID(t, st); !ok || v != "reg1" {
		t.Fatalf("expected the re-resolved identity persisted as reg1, got %q ok=%v", v, ok)
	}
}

// Zero active registers: the existing EnsureRegister self-heal creates the
// default one, and that becomes this till's persisted identity.
func TestResolveTillRegisterID_NoRegistersSelfHeals(t *testing.T) {
	ctx := context.Background()
	db := testRegisterIdentityDB(t)
	st := settings.NewStore(db)

	got, err := ResolveTillRegisterID(ctx, db, st)
	if err != nil {
		t.Fatalf("ResolveTillRegisterID: %v", err)
	}
	if got != "reg-default" {
		t.Fatalf("expected the EnsureRegister default id, got %q", got)
	}
	if v, ok := persistedRegisterID(t, st); !ok || v != "reg-default" {
		t.Fatalf("expected reg-default persisted, got %q ok=%v", v, ok)
	}
}

// Exactly one active register: that IS the till's register (only one
// possible answer, not a guess) — adopted, persisted, and STICKY: it must
// survive a second register appearing later, which is exactly the
// "identity survives restart and upgrade" acceptance criterion.
func TestResolveTillRegisterID_SingleRegisterAdoptedAndSticky(t *testing.T) {
	ctx := context.Background()
	db := testRegisterIdentityDB(t)
	st := settings.NewStore(db)
	if _, err := db.Exec(`INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveTillRegisterID(ctx, db, st)
	if err != nil {
		t.Fatalf("ResolveTillRegisterID: %v", err)
	}
	if got != "reg1" {
		t.Fatalf("expected the sole active register reg1, got %q", got)
	}
	if v, ok := persistedRegisterID(t, st); !ok || v != "reg1" {
		t.Fatalf("expected reg1 persisted, got %q ok=%v", v, ok)
	}

	// A second register appears (another till gets set up on this shop):
	// this till's already-established identity must NOT become ambiguous.
	if _, err := db.Exec(`INSERT INTO registers(id,name,is_active) VALUES('reg2','Back Till',1)`); err != nil {
		t.Fatal(err)
	}
	got, err = ResolveTillRegisterID(ctx, db, st)
	if err != nil {
		t.Fatalf("ResolveTillRegisterID after second register appeared: %v", err)
	}
	if got != "reg1" {
		t.Fatalf("expected the established identity reg1 to stick, got %q", got)
	}
}

// Two-or-more active registers with nothing persisted is genuinely
// ambiguous — the loud failure the ut-docs#268 decision requires for
// writes, and nothing may be persisted (a later explicit choice in
// Settings must not race a guess).
func TestResolveTillRegisterID_MultipleRegistersUnsetIsAmbiguous(t *testing.T) {
	ctx := context.Background()
	db := testRegisterIdentityDB(t)
	st := settings.NewStore(db)
	for _, ins := range []string{
		`INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`,
		`INSERT INTO registers(id,name,is_active) VALUES('reg2','Back Till',1)`,
	} {
		if _, err := db.Exec(ins); err != nil {
			t.Fatal(err)
		}
	}

	_, err := ResolveTillRegisterID(ctx, db, st)
	if !errors.Is(err, ErrRegisterIdentityAmbiguous) {
		t.Fatalf("expected ErrRegisterIdentityAmbiguous, got %v", err)
	}
	if v, ok := persistedRegisterID(t, st); ok {
		t.Fatalf("expected nothing persisted on the ambiguous branch, got %q", v)
	}
}
