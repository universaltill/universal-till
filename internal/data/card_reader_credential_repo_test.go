package data

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ut-docs#6538 R1a (ADR-0198 §6.2/6.3): the identity of a paired phone card
// reader. One row per reader in card_reader_credentials, written in the same
// transaction as its card_readers row; only the SHA-256 of the bearer is kept.

func newCredTestRepos(t *testing.T) (*CardReaderRepo, *CardReaderCredentialRepo, *PairingRepo) {
	t.Helper()
	d := openMigratedDB(t, "readercred.db")
	return NewCardReaderRepo(d.DB), NewCardReaderCredentialRepo(d.DB), NewPairingRepo(d.DB)
}

func pairedPhone(name, ref string) CardReader {
	return CardReader{Name: name, MethodKey: "pay", PluginID: "p.pay", ReaderRef: ref, Kind: CardReaderKindPhone, Source: CardReaderSourcePaired}
}

func TestCreatePaired_WritesReaderAndCredentialTogether(t *testing.T) {
	ctx := context.Background()
	readers, creds, _ := newCredTestRepos(t)
	c, err := readers.CreatePaired(ctx, pairedPhone("Front phone", "rd_a1"), NewReaderCredential{BearerHash: "h1", PinSPKI: "pin-1"})
	if err != nil {
		t.Fatalf("CreatePaired: %v", err)
	}
	if c.Kind != CardReaderKindPhone || c.Source != CardReaderSourcePaired {
		t.Fatalf("row = kind %q source %q, want phone/paired", c.Kind, c.Source)
	}
	got, ok, err := creds.ByBearerHashAt(ctx, "h1", time.Now())
	if err != nil || !ok {
		t.Fatalf("ByBearerHashAt = %v %v, want found", ok, err)
	}
	if got.ReaderID != c.ID || got.PinSPKI != "pin-1" || got.Name != "Front phone" {
		t.Fatalf("credential = %+v", got)
	}
}

func TestCreatePaired_RefusesNonPhoneKindAndEmptyHash(t *testing.T) {
	ctx := context.Background()
	readers, _, _ := newCredTestRepos(t)
	cloud := pairedPhone("Cloud", "rd_b1")
	cloud.Kind = CardReaderKindCloud
	if _, err := readers.CreatePaired(ctx, cloud, NewReaderCredential{BearerHash: "h"}); !errors.Is(err, ErrCardReaderKind) {
		t.Fatalf("cloud kind err = %v, want ErrCardReaderKind", err)
	}
	if _, err := readers.CreatePaired(ctx, pairedPhone("NoHash", "rd_b2"), NewReaderCredential{}); err == nil {
		t.Fatal("an empty bearer hash must be refused")
	}
	if n, _ := readers.ListActive(ctx); len(n) != 0 {
		t.Fatalf("a refused enrolment left %d reader rows (not one transaction)", len(n))
	}
}

func TestCreatePaired_CapIsSharedWithRegistry(t *testing.T) {
	ctx := context.Background()
	readers, _, _ := newCredTestRepos(t)
	for i := 0; i < MaxActiveCardReaders; i++ {
		ref := "rd_" + strings.Repeat("c", 3) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		name := "Phone " + ref
		if _, err := readers.CreatePaired(ctx, pairedPhone(name, ref), NewReaderCredential{BearerHash: "hash-" + ref}); err != nil {
			t.Fatalf("pair %d: %v", i, err)
		}
	}
	if _, err := readers.CreatePaired(ctx, pairedPhone("One too many", "rd_zzzz"), NewReaderCredential{BearerHash: "hash-over"}); !errors.Is(err, ErrCardReaderLimit) {
		t.Fatalf("33rd reader err = %v, want ErrCardReaderLimit", err)
	}
}

func TestRevokedReaderBearerNotFound(t *testing.T) {
	ctx := context.Background()
	readers, creds, _ := newCredTestRepos(t)
	c, _ := readers.CreatePaired(ctx, pairedPhone("P", "rd_r1"), NewReaderCredential{BearerHash: "hr"})
	if err := creds.Revoke(ctx, c.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok, _ := creds.ByBearerHashAt(ctx, "hr", time.Now()); ok {
		t.Fatal("a revoked reader's bearer must not authenticate")
	}
}

func TestSoftRemoveRevokesTheCredentialInTheSameStep(t *testing.T) {
	ctx := context.Background()
	readers, creds, _ := newCredTestRepos(t)
	c, _ := readers.CreatePaired(ctx, pairedPhone("P", "rd_s1"), NewReaderCredential{BearerHash: "hs"})
	if _, err := readers.SoftRemove(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := creds.ByBearerHashAt(ctx, "hs", time.Now()); ok {
		t.Fatal("removing the reader row must revoke its bearer")
	}
}

func TestReaderBearerRotation_GraceThenGone(t *testing.T) {
	ctx := context.Background()
	readers, creds, _ := newCredTestRepos(t)
	c, _ := readers.CreatePaired(ctx, pairedPhone("P", "rd_t1"), NewReaderCredential{BearerHash: "old"})
	now := time.Now()
	ok, err := creds.RotateBearer(ctx, c.ID, "old", "new", now)
	if err != nil || !ok {
		t.Fatalf("RotateBearer = %v %v", ok, err)
	}
	if _, found, _ := creds.ByBearerHashAt(ctx, "new", now); !found {
		t.Fatal("the new bearer must authenticate")
	}
	if _, found, _ := creds.ByBearerHashAt(ctx, "old", now.Add(time.Minute)); !found {
		t.Fatal("the old bearer must still work inside the grace window")
	}
	if _, found, _ := creds.ByBearerHashAt(ctx, "old", now.Add(ReaderBearerGrace+time.Minute)); found {
		t.Fatal("the old bearer must stop working after the grace window")
	}
	// A stale presenter cannot rotate again (the old bearer is not current).
	if ok, _ := creds.RotateBearer(ctx, c.ID, "old", "newer", now); ok {
		t.Fatal("only the current bearer may rotate")
	}
}

func TestApproveWithRoleRefusesReaderToTillAndBack(t *testing.T) {
	ctx := context.Background()
	_, _, pairings := newCredTestRepos(t)
	rid, err := pairings.CreatePendingRequestWithRole(ctx, "Phone", "c1", TillRoleCardReader, "pin", time.Minute)
	if err != nil {
		t.Fatalf("create reader request: %v", err)
	}
	for _, role := range []string{TillRoleAdditional, TillRoleSatellite} {
		if err := pairings.ApproveWithRole(ctx, rid, "", role, time.Minute); !errors.Is(err, ErrRoleMismatch) {
			t.Fatalf("reader request approved as %q: err = %v, want ErrRoleMismatch", role, err)
		}
	}
	tid, _ := pairings.CreatePendingRequestWithRole(ctx, "Till", "c2", TillRoleAdditional, "", time.Minute)
	if err := pairings.ApproveWithRole(ctx, tid, "tok", TillRoleCardReader, time.Minute); !errors.Is(err, ErrRoleMismatch) {
		t.Fatalf("till request approved as card_reader: err = %v, want ErrRoleMismatch", err)
	}
	// Neither refusal changed the row: still pending.
	if row, ok, _ := pairings.GetByID(ctx, rid); !ok || row.Status != "pending" || row.RequestedRole != TillRoleCardReader {
		t.Fatalf("a refused approval altered the reader request: %+v", row)
	}
	// The matching role (or none) approves.
	if err := pairings.ApproveWithRole(ctx, rid, "", "", time.Minute); err != nil {
		t.Fatalf("approving a reader request with no role override: %v", err)
	}
}

func TestEmptyRoleNeverReader(t *testing.T) {
	if ValidPairingRole("") {
		t.Fatal("an empty role must never be a valid pairing role, least of all a reader")
	}
	if ValidTillRole(TillRoleCardReader) {
		t.Fatal("card_reader is a pairing role, never a till role: it must not enrol a till")
	}
	if !ValidPairingRole(TillRoleCardReader) || !ValidPairingRole(TillRoleAdditional) || !ValidPairingRole(TillRoleSatellite) {
		t.Fatal("the three pairing roles must validate")
	}
}

func TestNoCardDataColumnsInReaderTables(t *testing.T) {
	d := openMigratedDB(t, "readercols.db")
	bad := regexp.MustCompile(`(?i)(pan|card_?number|cvv|cvc|track|expiry|exp_?month|exp_?year|last4|brand|client_secret|secret|token_plain|bearer$|bearer_plain)`)
	for _, table := range []string{"card_reader_credentials", "card_readers"} {
		rows, err := d.DB.Query(`SELECT name FROM pragma_table_info('` + table + `')`)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var col string
			_ = rows.Scan(&col)
			n++
			if bad.MatchString(col) {
				t.Errorf("%s.%s looks like card data or a plaintext secret (ADR-0127, ADR-0198 §6.3)", table, col)
			}
		}
		rows.Close()
		if n == 0 {
			t.Fatalf("table %s has no columns (migration missing)", table)
		}
	}
}

// The credentials table is till-local like card_readers: nothing but its own
// repository (and the admin-table classification) may name it.
var credTableRef = regexp.MustCompile(`(?i)\b(from|into|update|join|table)\s+card_reader_credentials\b`)

func TestCardReaderCredentialsOnlyInRepo(t *testing.T) {
	allowed := map[string]bool{
		"card_reader_credential_repo.go": true,
		"sync_admin_repo.go":             true, // classifies the table as NOT synced
	}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || allowed[filepath.Base(path)] {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if credTableRef.Match(b) {
			t.Errorf("%s queries card_reader_credentials: till-local, only its repository may", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := nonAdminTables["card_reader_credentials"]; !ok {
		t.Fatal("card_reader_credentials must be classified in nonAdminTables with its reason")
	}
}
