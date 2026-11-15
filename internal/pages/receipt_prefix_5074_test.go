package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#5074 S0: a receipt prefix (T<n>-) the main till granted is never
// granted to another till, whatever happened since: the till was revoked,
// withdrew its enrolment, the main till restarted, or another till was
// promoted to main till. Two tills on one prefix mint the same receipt_no;
// the journal replay then hits UNIQUE(sales.receipt_no), which is not
// quarantined, so that till's sales never reach the main till again. Real
// migrated temp DBs and the real enrolment and revoke routes.

type prefixShop struct {
	main *common.Deps
	path string
	mux  *http.ServeMux
}

func newPrefixShop(t *testing.T) *prefixShop {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	s := &prefixShop{}
	s.main, s.path = newSyncDepsWithPath(t, "main.db")
	s.mux = http.NewServeMux()
	registerSyncAPI(s.mux, s.main)
	return s
}

// enrol enrols a till called name (asking for its own prefix when requested
// is set, as a re-joining till does) and returns its id, bearer and number.
func (s *prefixShop) enrol(t *testing.T, name, requested string) (string, string, int) {
	t.Helper()
	_, token, err := decodeEnrollCode(issueEnrolCode(t, s.mux, "http://192.168.1.10:8080"))
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]string{"token": token, "name": name}
	if requested != "" {
		in["receipt_prefix"] = requested
	}
	body, _ := json.Marshal(in)
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enrol %q: %d %s", name, rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			TillID string `json:"till_id"`
			Bearer string `json:"bearer"`
			TillNo int    `json:"till_no"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("enrol %q: %v %s", name, err, rec.Body.String())
	}
	var stored string
	if err := s.main.Db.QueryRow(`SELECT receipt_prefix FROM tills WHERE id = ?`, out.Data.TillID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("T%d-", out.Data.TillNo); stored != want {
		t.Fatalf("enrol %q: answered till %d but recorded prefix %q", name, out.Data.TillNo, stored)
	}
	return out.Data.TillID, out.Data.Bearer, out.Data.TillNo
}

func (s *prefixShop) revoke(t *testing.T, id string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/tills/"+id+"/revoke", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code >= 300 {
		t.Fatalf("revoke %s: %d %s", id, rec.Code, rec.Body.String())
	}
	if tillExists(t, s.main, id) {
		t.Fatalf("revoke %s: the till is still enrolled", id)
	}
}

func wantTillNo(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: till number %d (T%d-), want %d (T%d-)", what, got, got, want, want)
	}
}

// The brief's reproduction: add till 2, remove it, add another. The new till
// must not get a number another till already had (nor the main till's 1).
func TestS0_RevokedTillsNumberIsNeverGrantedAgain(t *testing.T) {
	s := newPrefixShop(t)
	a, _, n := s.enrol(t, "Till 2", "")
	wantTillNo(t, "first till", n, 2)
	s.revoke(t, a)
	_, _, n = s.enrol(t, "Till 3", "")
	wantTillNo(t, "after revoking till 2", n, 3)
}

// Two tills, the newest is revoked before any of its sales reached the main
// till (it was offline): the next till must not take its number.
func TestS0_RevokedNewestTillWithNoSalesHereKeepsItsNumber(t *testing.T) {
	s := newPrefixShop(t)
	s.enrol(t, "Till 2", "")
	b, _, n := s.enrol(t, "Till 3", "")
	wantTillNo(t, "second till", n, 3)
	s.revoke(t, b)
	_, _, n = s.enrol(t, "Till 4", "")
	wantTillNo(t, "after revoking till 3", n, 4)
	// The revoked till re-joins (a mistaken revoke): it keeps its own number,
	// so its queued T3- sales go on numbering and collide with nobody.
	_, _, n = s.enrol(t, "Till 3 again", "T3-")
	wantTillNo(t, "revoked till re-joining", n, 3)
	_, _, n = s.enrol(t, "Till 5", "")
	wantTillNo(t, "next fresh till", n, 5)
}

// A join that failed after enrolling withdraws its enrolment (ut-docs#4566).
// The number it was given is spent all the same.
func TestS0_WithdrawnEnrolmentsNumberIsSpent(t *testing.T) {
	s := newPrefixShop(t)
	_, bearer, n := s.enrol(t, "Till 2", "")
	wantTillNo(t, "first till", n, 2)
	if rec := postWithdraw(t, s.mux, bearer); rec.Code != http.StatusNoContent {
		t.Fatalf("withdraw: %d %s", rec.Code, rec.Body.String())
	}
	_, _, n = s.enrol(t, "Till 2", "")
	wantTillNo(t, "retry after a withdrawn join", n, 3)
}

// The record survives a restart of the main till (it is in the database,
// not in memory).
func TestS0_NumbersStaySpentAcrossARestart(t *testing.T) {
	s := newPrefixShop(t)
	s.enrol(t, "Till 2", "")
	b, _, _ := s.enrol(t, "Till 3", "")
	s.revoke(t, b)
	if err := s.main.Db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := appdb.Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	s.main.Db = reopened.DB
	s.main.Settings = settings.NewStore(reopened.DB)
	s.mux = http.NewServeMux()
	registerSyncAPI(s.mux, s.main)
	_, _, n := s.enrol(t, "Till 4", "")
	wantTillNo(t, "after a restart", n, 4)
}

// Sales moved out by "Clear transaction history" (ADR-0042) still carry
// their till's prefix: it stays spent.
func TestS0_ArchivedSalesKeepTheirPrefixSpent(t *testing.T) {
	s := newPrefixShop(t)
	mustExecPages(t, s.main, `INSERT INTO reset_batches (id, created_at) VALUES ('b1', '2026-10-01T00:00:00Z')`)
	mustExecPages(t, s.main, `
INSERT INTO sales_archive (id, receipt_no, status, sale_type, tender_type, offline, sync_status, sync_attempts,
  currency, subtotal, discount_total, tax_total, total, rounding, created_at, till_id, service_charge_amount,
  order_type, order_status, reset_batch_id)
VALUES ('old', 'T6-000000012', 'completed', 'sale', 'cash', 0, 'synced', 0, 'GBP', 1, 0, 0, 1, 0,
  '2026-09-30T10:00:00Z', 'gone-till', 0, '', '', 'b1')`)
	_, _, n := s.enrol(t, "Till 7", "")
	wantTillNo(t, "with T6- only in the archive", n, 7)
}

// A till promoted to main till (it was T3-) continues the shop's sequence:
// the record travelled to it in the admin bundle, and its own prefix is
// spent even with an empty roster.
func TestS0_PromotedTillContinuesTheShopsNumbers(t *testing.T) {
	s := newPrefixShop(t)
	ctx := context.Background()
	// As a replica it held T3- and had received the old main till's record
	// (the shop had numbered up to 5). Promote keeps sync.receipt_prefix.
	for k, v := range map[string]string{
		"sync.primary_url":        "http://192.168.1.10:8080",
		"sync.receipt_prefix":     "T3-",
		"shop.till_no_high_water": "5",
	} {
		if err := s.main.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := data.NewSettingsRepo(s.main.Db).ClearReplicaIdentity(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, n := s.enrol(t, "Till 6", "")
	wantTillNo(t, "first till enrolled on the promoted main till", n, 6)
	// Without the record (an old main till's bundle) its own prefix still
	// counts: a re-joining till is never handed T3-, the promoted till's own.
	if err := s.main.Settings.Set(ctx, "shop.till_no_high_water", ""); err != nil {
		t.Fatal(err)
	}
	mustExecPages(t, s.main, `DELETE FROM tills`)
	_, _, n = s.enrol(t, "Till X", "T3-")
	if n <= 3 {
		t.Fatalf("a re-joining till was given %d, at or below the promoted main till's own T3-", n)
	}
}

// A re-joining till that asks for a number above anything granted keeps it,
// and the record moves past it.
func TestS0_RequestedNumberAboveTheRecordRaisesIt(t *testing.T) {
	s := newPrefixShop(t)
	_, _, n := s.enrol(t, "Till 9", "T9-")
	wantTillNo(t, "re-joining till 9", n, 9)
	_, _, n = s.enrol(t, "Till 10", "")
	wantTillNo(t, "next fresh till", n, 10)
}

func mustExecPages(t *testing.T, d *common.Deps, q string, args ...any) {
	t.Helper()
	if _, err := d.Db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
