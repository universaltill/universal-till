package recovery

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// requireLoopback is the only thing standing between "any LAN device can
// read today's sales" and "only this device can" (independent review,
// ut-docs#1436) — cfg.ListenAddr defaults to every interface, and recovery
// mode has no session store to authenticate against. Tested directly via
// httptest.NewRequest's settable RemoteAddr, not a real network call —
// there's no way to originate a genuinely non-loopback connection in a test
// sandbox, and this is the standard way to test this class of middleware.
func TestRequireLoopback_RefusesNonLoopbackRemoteAddr(t *testing.T) {
	called := false
	h := requireLoopback(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/recovery/safe-mode", nil)
	req.RemoteAddr = "203.0.113.5:54321" // TEST-NET-3 (RFC 5737), not loopback
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if called {
		t.Fatal("the wrapped handler ran for a non-loopback remote address")
	}
}

func TestRequireLoopback_AllowsLoopback(t *testing.T) {
	called := false
	h := requireLoopback(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusOK) })

	for _, addr := range []string{"127.0.0.1:54321", "[::1]:54321"} {
		called = false
		req := httptest.NewRequest(http.MethodGet, "/recovery/safe-mode", nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("addr %s: status = %d, want 200", addr, rec.Code)
		}
		if !called {
			t.Fatalf("addr %s: the wrapped handler did not run for a loopback remote address", addr)
		}
	}
}

func TestCSVSafe_PrefixesFormulaLeadingCharacters(t *testing.T) {
	// Mirrors internal/pages/csv_export.go's own test coverage — this is a
	// local duplicate of that exact convention (see csvSafe's doc comment
	// for why it's duplicated rather than imported).
	cases := map[string]string{
		"=cmd|'/c calc'!A1": "'=cmd|'/c calc'!A1",
		"+1+1":              "'+1+1",
		"@SUM(A1:A2)":       "'@SUM(A1:A2)",
		"R-0001":            "R-0001",
		"":                  "",
		"-":                 "-", // this codebase's own "no entity ID" sentinel
	}
	for in, want := range cases {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

// The exact gap independent review found live: db.OpenReadOnly succeeding
// only proves the SQLite file is readable, not that ListSalesJournal's
// schema requirements (tills @ migration 014, sales.till_id @ 015, of 78)
// are met. A migration failure anywhere before those leaves a perfectly
// openable database this query still can't run against.
func TestProbeSafeMode_FailsAgainstPreTillsSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	full, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Simulate a migration failure that stopped before `tills` existed —
	// drop it from an otherwise fully-migrated database.
	if _, err := full.Exec(`DROP TABLE tills`); err != nil {
		t.Fatalf("drop tills: %v", err)
	}
	if err := full.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ro, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()

	if probeSafeMode(ro.DB) {
		t.Fatal("probeSafeMode reported success against a database missing the `tills` table ListSalesJournal joins — it must actually run the query, not just check the file opens")
	}
}

func TestProbeSafeMode_SucceedsAgainstAFullyMigratedDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	full, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = full.Close()

	ro, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()

	if !probeSafeMode(ro.DB) {
		t.Fatal("probeSafeMode reported failure against a genuinely fully-migrated, fresh database")
	}
}

func TestRetryHandler_ThrottlesRapidRequests(t *testing.T) {
	retry := make(chan struct{}, 1)
	h := retryHandler(retry)

	req := httptest.NewRequest(http.MethodPost, "/api/recovery/retry", nil)
	rec1 := httptest.NewRecorder()
	h(rec1, req)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first request status = %d, want 202", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	h(rec2, httptest.NewRequest(http.MethodPost, "/api/recovery/retry", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("immediate second request status = %d, want 429", rec2.Code)
	}

	// Exactly one signal made it onto the channel — the throttled request
	// must not have queued a second one behind it.
	select {
	case <-retry:
	default:
		t.Fatal("expected one retry signal on the channel from the first request")
	}
	select {
	case <-retry:
		t.Fatal("a second retry signal leaked through despite being throttled")
	default:
	}
}
