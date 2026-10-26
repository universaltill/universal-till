package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/lantls"
)

// Manager balance correction (ut-docs#4253): POST /vouchers/{id}/adjust from
// the voucher detail page. Harness: newVoucherImportTestMux (real migrated
// DB, real auth.Service, seeded manager "m1").

func vAdjust(t *testing.T, mux *http.ServeMux, id string, form url.Values, user *auth.User, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/vouchers/"+url.PathEscape(id)+"/adjust", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func adjForm(adjID, expected, newBal, reason string) url.Values {
	return url.Values{"adjustment_id": {adjID}, "expected_balance": {expected}, "new_balance": {newBal}, "reason": {reason}}
}

func vBalance(t *testing.T, repo *data.POSRepo, id string) data.Voucher {
	t.Helper()
	v, err := repo.GetVoucherBalance(context.Background(), nil, id)
	if err != nil {
		t.Fatalf("GetVoucherBalance %s: %v", id, err)
	}
	return v
}

const adjUUID1 = "6f1c1b2e-6a43-4c55-9d4e-0d3f3a1d2b01"

func TestVoucherAdjust_ForbiddenForCashierAndAnonymous(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
	for _, u := range []*auth.User{nil, &cashier} {
		rec := vAdjust(t, mux, "GS-1", adjForm(adjUUID1, "1000", "5", "typo"), u, true)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST as %v = %d, want 403", u, rec.Code)
		}
	}
	if v := vBalance(t, repo, "GS-1"); v.BalanceMinor != 1000 {
		t.Fatalf("forbidden POST changed the balance to %d", v.BalanceMinor)
	}
}

func TestVoucherAdjust_SuccessRedirectsAndRecords(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})

	// htmx: HX-Redirect back to the detail page. A comma decimal is fine.
	rec := vAdjust(t, mux, "GS-1", adjForm(adjUUID1, "1000", "4,50", "  keyed 10 instead of 4.50  "), &vManager, true)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/vouchers/GS-1?adjusted=1" {
		t.Fatalf("htmx success = %d, HX-Redirect %q (%s)", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	v := vBalance(t, repo, "GS-1")
	if v.BalanceMinor != 450 || v.Status != "active" {
		t.Fatalf("after correction: %+v", v)
	}
	// A double-submit of the same form (same adjustment id) applies nothing.
	rec = vAdjust(t, mux, "GS-1", adjForm(adjUUID1, "1000", "4,50", "keyed 10 instead of 4.50"), &vManager, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("double submit = %d (%s)", rec.Code, rec.Body.String())
	}
	hist, err := repo.VoucherTransactions(context.Background(), "GS-1")
	if err != nil {
		t.Fatal(err)
	}
	var adj []data.VoucherHistoryRow
	for _, h := range hist {
		if h.Type == "adjustment" {
			adj = append(adj, h)
		}
	}
	if len(adj) != 1 || adj[0].AmountMinor != -550 || adj[0].Reason != "keyed 10 instead of 4.50" || adj[0].ActorUserID != "m1" {
		t.Fatalf("adjustment rows = %+v", adj)
	}

	// Plain POST (no htmx): 303 back to the page.
	rec = vAdjust(t, mux, "GS-1", adjForm("6f1c1b2e-6a43-4c55-9d4e-0d3f3a1d2b02", "450", "0", "spent elsewhere"), &vManager, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/vouchers/GS-1?adjusted=1" {
		t.Fatalf("plain success = %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	if v := vBalance(t, repo, "GS-1"); v.BalanceMinor != 0 || v.Status != "redeemed" {
		t.Fatalf("to zero: %+v", v)
	}

	// The page now shows the correction in the history, with its reason,
	// and the done note.
	page := vGet(t, mux, "/vouchers/GS-1?adjusted=1", &vManager, false)
	body := page.Body.String()
	for _, want := range []string{"Adjustment", "keyed 10 instead of 4.50", "Balance corrected."} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail page missing %q", want)
		}
	}
}

func TestVoucherAdjust_RefusalsShowInlineMessage(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GS-SP", OriginalAmountMinor: 1000, Purpose: data.VoucherPurposeSingle, CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GS-VOID", OriginalAmountMinor: 1000, Status: "void", CreatedAt: "2026-10-01T10:00:00Z"})

	cases := []struct {
		name, id string
		form     url.Values
		code     int
		want     string
	}{
		{"bad amount", "GS-1", adjForm(adjUUID1, "1000", "abc", "x"), http.StatusBadRequest, "Enter the new balance as an amount"},
		{"negative amount", "GS-1", adjForm(adjUUID1, "1000", "-1", "x"), http.StatusBadRequest, "Enter the new balance as an amount"},
		{"above ceiling", "GS-1", adjForm(adjUUID1, "1000", "99999999", "x"), http.StatusBadRequest, "Enter the new balance as an amount"},
		{"bad expected", "GS-1", adjForm(adjUUID1, "x", "5", "x"), http.StatusBadRequest, "Reload the page and try again"},
		{"missing expected", "GS-1", adjForm(adjUUID1, "", "5", "x"), http.StatusBadRequest, "Reload the page and try again"},
		{"missing adjustment id", "GS-1", adjForm("", "1000", "5", "x"), http.StatusBadRequest, "Reload the page and try again"},
		{"no reason", "GS-1", adjForm(adjUUID1, "1000", "5", " "), http.StatusBadRequest, "Enter a reason"},
		{"long reason", "GS-1", adjForm(adjUUID1, "1000", "5", strings.Repeat("a", 201)), http.StatusBadRequest, "Enter a reason"},
		{"no change", "GS-1", adjForm(adjUUID1, "1000", "10", "x"), http.StatusConflict, "same as the current balance"},
		{"balance changed", "GS-1", adjForm(adjUUID1, "900", "5", "x"), http.StatusConflict, "changed while you were looking"},
		{"single purpose", "GS-SP", adjForm(adjUUID1, "1000", "5", "x"), http.StatusConflict, "taxed when it was sold"},
		{"void", "GS-VOID", adjForm(adjUUID1, "0", "5", "x"), http.StatusConflict, "voided voucher"},
		{"unknown", "GS-NOPE", adjForm(adjUUID1, "0", "5", "x"), http.StatusNotFound, "no gift voucher with this number"},
		{"bad adjustment id", "GS-1", adjForm("not-a-uuid", "1000", "5", "x"), http.StatusBadRequest, "Reload the page and try again"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := vAdjust(t, mux, tc.id, tc.form, &vManager, true)
			if rec.Code != tc.code {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("content-type %q: an inline message must be an HTML fragment so app.js swaps it in", ct)
			}
			if !strings.Contains(rec.Body.String(), tc.want) || !strings.Contains(rec.Body.String(), `role="alert"`) {
				t.Fatalf("body %q missing %q", rec.Body.String(), tc.want)
			}
		})
	}
	if v := vBalance(t, repo, "GS-1"); v.BalanceMinor != 1000 {
		t.Fatalf("refusals changed GS-1 to %d", v.BalanceMinor)
	}
}

func TestVoucherDetail_CorrectionSectionOnlyForCorrectableVouchers(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GS-SP", OriginalAmountMinor: 1000, Purpose: data.VoucherPurposeSingle, CreatedAt: "2026-10-01T10:00:00Z"})
	vSeed(t, repo, data.Voucher{ID: "GS-VOID", OriginalAmountMinor: 1000, Status: "void", CreatedAt: "2026-10-01T10:00:00Z"})

	body := vGet(t, mux, "/vouchers/GS-1", &vManager, false).Body.String()
	for _, want := range []string{`hx-post="/vouchers/GS-1/adjust"`, `name="new_balance"`, `inputmode="decimal"`, `name="expected_balance" value="1000"`, `maxlength="200"`, `name="adjustment_id"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("active voucher page missing %q", want)
		}
	}
	start := strings.Index(body, `id="voucher-adjust"`)
	end := strings.Index(body, `id="voucher-adjust-msg"`)
	if start < 0 || end < start {
		t.Fatal("correction card / its inline message slot missing")
	}
	if section := body[start:end]; strings.Contains(section, "showModal") || strings.Contains(section, "<dialog") {
		t.Fatal("the correction form must not open a modal")
	}
	for id, note := range map[string]string{"GS-SP": "taxed when it was sold", "GS-VOID": "A voided voucher"} {
		body := vGet(t, mux, "/vouchers/"+id, &vManager, false).Body.String()
		if strings.Contains(body, `name="new_balance"`) {
			t.Fatalf("%s: correction form rendered for an uncorrectable voucher", id)
		}
		if !strings.Contains(body, note) {
			t.Fatalf("%s: missing the why-not note %q", id, note)
		}
	}
}

// --- Additional till (replica): the correction is made on the main till's
// ledger first, then mirrored locally.

func TestVoucherAdjust_ReplicaPrimaryUnreachableRefuses(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})
	deadURL := unreachableURL()
	setReplicaSettings(t, d.Settings, deadURL, "b-replica")

	rec := vAdjust(t, mux, "GS-1", adjForm(adjUUID1, "1000", "5", "typo"), &vManager, true)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "reach the main till") || strings.Contains(rec.Body.String(), "may or may not have been saved") {
		t.Fatalf("unreachable (dial refused) primary = %d %q, want 503 + the refusal message", rec.Code, rec.Body.String())
	}
	if v := vBalance(t, repo, "GS-1"); v.BalanceMinor != 1000 {
		t.Fatalf("a local-only correction happened: balance %d", v.BalanceMinor)
	}
}

func TestVoucherAdjust_ReplicaPrimaryErrorRefuses(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	setReplicaSettings(t, d.Settings, broken.URL, "b-replica")

	// A 5xx is ambiguous: the main till may have committed the correction
	// before failing, so the manager is told to check, not that nothing
	// happened (review MAJOR-2).
	rec := vAdjust(t, mux, "GS-1", adjForm(adjUUID1, "1000", "5", "typo"), &vManager, true)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "confirm the correction — it may or may not have been saved") || strings.Contains(rec.Body.String(), "reach the main till") {
		t.Fatalf("primary 500 = %d %q, want 502 + the may-or-may-not message", rec.Code, rec.Body.String())
	}
	if v := vBalance(t, repo, "GS-1"); v.BalanceMinor != 1000 {
		t.Fatalf("a local-only correction happened: balance %d", v.BalanceMinor)
	}
}

func TestVoucherAdjust_ReplicaPrimarySuccessMirrorsLocally(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	primary, primaryDp, primaryRepo, calls := newRecordingVoucherPrimaryServer(t, "b-replica")
	setReplicaSettings(t, d.Settings, primary.URL, "b-replica")

	// Unknown locally: issued at another till.
	seedPrimaryVoucher(t, primaryRepo, "GS-ELSE", "Bob", 1000)
	rec := vAdjust(t, mux, "GS-ELSE", adjForm(adjUUID1, "1000", "7.50", "damaged card"), &vManager, true)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") == "" {
		t.Fatalf("replica correction = %d (%s)", rec.Code, rec.Body.String())
	}
	if pv := vBalance(t, primaryRepo, "GS-ELSE"); pv.BalanceMinor != 750 {
		t.Fatalf("primary balance = %d, want 750", pv.BalanceMinor)
	}
	if lv := vBalance(t, repo, "GS-ELSE"); lv.BalanceMinor != 750 || lv.HolderLabel != "Bob" || lv.OriginalAmountMinor != 1000 {
		t.Fatalf("local mirror = %+v, want 750 with the primary's facts", lv)
	}
	var localAmt int64
	var localReason string
	if err := d.Db.QueryRow(`SELECT amount, reason FROM voucher_transactions WHERE id = ? AND type = 'adjustment'`, adjUUID1).Scan(&localAmt, &localReason); err != nil {
		t.Fatalf("local adjustment row (same id as the primary's): %v", err)
	}
	if localAmt != -250 || localReason != "damaged card" {
		t.Fatalf("local row amount %d reason %q", localAmt, localReason)
	}
	var primaryActor string
	if err := primaryDp.Db.QueryRow(`SELECT actor_user_id FROM voucher_transactions WHERE id = ?`, adjUUID1).Scan(&primaryActor); err != nil || primaryActor != "m1" {
		t.Fatalf("primary actor = %q (%v), want m1", primaryActor, err)
	}
	var sawAdjust bool
	for _, c := range calls.all() {
		if strings.HasPrefix(c, "POST /api/sync/vouchers/GS-ELSE/adjust ") {
			sawAdjust = true
		}
	}
	if !sawAdjust {
		t.Fatalf("replica never called the primary's /adjust: %v", calls.all())
	}

	// Known locally at a STALE balance (a redemption here hasn't reached
	// the main till's view the same way): the local row lands on the
	// primary's resulting balance, and the local ledger row records the
	// delta against the local prior balance so local history adds up.
	seedPrimaryVoucher(t, primaryRepo, "GS-BOTH", "", 1000)
	vSeed(t, repo, data.Voucher{ID: "GS-BOTH", OriginalAmountMinor: 1000, BalanceMinor: 800, Status: "active", CreatedAt: "2026-10-01T10:00:00Z"})
	rec = vAdjust(t, mux, "GS-BOTH", adjForm("6f1c1b2e-6a43-4c55-9d4e-0d3f3a1d2b03", "1000", "6", "top-up"), &vManager, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("replica correction (stale local) = %d (%s)", rec.Code, rec.Body.String())
	}
	if lv := vBalance(t, repo, "GS-BOTH"); lv.BalanceMinor != 600 {
		t.Fatalf("local balance = %d, want the primary's 600", lv.BalanceMinor)
	}
	if err := d.Db.QueryRow(`SELECT amount FROM voucher_transactions WHERE id = '6f1c1b2e-6a43-4c55-9d4e-0d3f3a1d2b03'`).Scan(&localAmt); err != nil || localAmt != -200 {
		t.Fatalf("local delta = %d (%v), want -200 (600 − local 800)", localAmt, err)
	}
	var primaryAmt int64
	if err := primaryDp.Db.QueryRow(`SELECT amount FROM voucher_transactions WHERE id = '6f1c1b2e-6a43-4c55-9d4e-0d3f3a1d2b03'`).Scan(&primaryAmt); err != nil || primaryAmt != -400 {
		t.Fatalf("primary delta = %d (%v), want -400", primaryAmt, err)
	}
}

func TestVoucherAdjust_ReplicaPrimaryConflictSurfacesMessage(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	primary, _, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	setReplicaSettings(t, d.Settings, primary.URL, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-1", "", 900)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})

	rec := vAdjust(t, mux, "GS-1", adjForm(adjUUID1, "1000", "5", "typo"), &vManager, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "changed while you were looking") {
		t.Fatalf("primary 409 = %d %q, want the balance-changed message", rec.Code, rec.Body.String())
	}
	if v := vBalance(t, repo, "GS-1"); v.BalanceMinor != 1000 {
		t.Fatalf("local balance changed to %d on a refused correction", v.BalanceMinor)
	}
	if v := vBalance(t, primaryRepo, "GS-1"); v.BalanceMinor != 900 {
		t.Fatalf("primary balance changed to %d on a refused correction", v.BalanceMinor)
	}
}

// On an additional till the form is pre-filled from the main till's live
// balance, so a stale local mirror can't make every correction fail as
// "balance changed".
func TestVoucherDetail_ReplicaFormUsesPrimaryBalance(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	primary, _, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	setReplicaSettings(t, d.Settings, primary.URL, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-1", "", 900)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})

	body := vGet(t, mux, "/vouchers/GS-1", &vManager, false).Body.String()
	if !strings.Contains(body, `name="expected_balance" value="900"`) {
		t.Fatalf("replica form should carry the main till's balance (900)")
	}
}

// MAJOR-2: a primary that received the request but never answered within
// the budget may have applied the correction — "may or may not", never
// "nothing changed".
func TestVoucherAdjust_ReplicaPrimaryTimeoutIsAmbiguous(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); hung.Close() })
	setReplicaSettings(t, d.Settings, hung.URL, "b-replica")
	orig := voucherProxyClient
	voucherProxyClient = lantls.NewPeerClient(200 * time.Millisecond)
	t.Cleanup(func() { voucherProxyClient = orig })

	rec := vAdjust(t, mux, "GS-1", adjForm(adjUUID1, "1000", "5", "typo"), &vManager, true)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "confirm the correction — it may or may not have been saved") {
		t.Fatalf("hung primary = %d %q, want 502 + the may-or-may-not message", rec.Code, rec.Body.String())
	}
	if v := vBalance(t, repo, "GS-1"); v.BalanceMinor != 1000 {
		t.Fatalf("a local-only correction happened: balance %d", v.BalanceMinor)
	}
}

// MAJOR-2: a 200 the replica can't read is ambiguous too.
func TestVoucherAdjust_ReplicaPrimaryMalformedOKIsAmbiguous(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":`))
	}))
	t.Cleanup(bad.Close)
	setReplicaSettings(t, d.Settings, bad.URL, "b-replica")

	rec := vAdjust(t, mux, "GS-1", adjForm(adjUUID1, "1000", "5", "typo"), &vManager, true)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "confirm the correction — it may or may not have been saved") {
		t.Fatalf("malformed 200 = %d %q, want 502 + the may-or-may-not message", rec.Code, rec.Body.String())
	}
}

// MINOR-1: the local mirror sets the local row to the main till's result
// even when the local row is a stale void (this till never saw the
// voucher reinstated) — the main till already decided.
func TestVoucherAdjust_ReplicaMirrorIgnoresStaleLocalState(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	primary, _, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	setReplicaSettings(t, d.Settings, primary.URL, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-1", "", 1000)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, Status: "void", CreatedAt: "2026-10-01T10:00:00Z"})

	rec := vAdjust(t, mux, "GS-1", adjForm(adjUUID1, "1000", "6", "typo"), &vManager, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("replica correction = %d (%s)", rec.Code, rec.Body.String())
	}
	if lv := vBalance(t, repo, "GS-1"); lv.BalanceMinor != 600 || lv.Status != "active" {
		t.Fatalf("local mirror = %+v, want the main till's 600 active", lv)
	}
}

// MINOR-5: with auth on, a session whose user id is empty can't make an
// audited correction (it would record no actor).
func TestVoucherAdjust_EmptySessionUserForbidden(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})
	noID := auth.User{ID: "", Role: "manager", DisplayName: "Ghost"}
	rec := vAdjust(t, mux, "GS-1", adjForm(adjUUID1, "1000", "5", "typo"), &noID, true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("empty user id = %d (%s), want 403", rec.Code, rec.Body.String())
	}
	if v := vBalance(t, repo, "GS-1"); v.BalanceMinor != 1000 {
		t.Fatalf("balance changed to %d", v.BalanceMinor)
	}
}

// MAJOR-2: a clean 4xx the replica doesn't recognise (here a 401 — the
// main till refused the bearer) returns before the main till's commit, so
// it is still "nothing changed", not "may or may not".
func TestVoucherAdjust_ReplicaPrimaryUnknown4xxIsDefinite(t *testing.T) {
	mux, d := newVoucherImportTestMux(t)
	repo := data.NewPOSRepo(d.Db)
	vSeed(t, repo, data.Voucher{ID: "GS-1", OriginalAmountMinor: 1000, CreatedAt: "2026-10-01T10:00:00Z"})
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"data":null,"error":"unauthorized"}`))
	}))
	t.Cleanup(refuse.Close)
	setReplicaSettings(t, d.Settings, refuse.URL, "b-replica")

	rec := vAdjust(t, mux, "GS-1", adjForm(adjUUID1, "1000", "5", "typo"), &vManager, true)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "reach the main till") {
		t.Fatalf("primary 401 = %d %q, want 503 + the refusal message", rec.Code, rec.Body.String())
	}
}
