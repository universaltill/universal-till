package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// newReceiptPrintInvoiceGateTestDeps wires the subsystems touched by this
// successor card (ut-docs#712): receipt_designer.go, print_api.go and
// invoice_page.go all convert from isManagerOrAuthOff to canPerform(d, r,
// action) here — no new action/migration, since #712's review of all 10
// call sites found each one already fits an EXISTING catalog action. 8 of
// the 10 (receipt designer x5, printer settings, print test, invoice
// seller identity) use "settings" — each is a manager-only config page or
// /api/settings/*-namespaced, and the ones with InsertAudit already log
// under category "settings". The other 2 — GET /invoices and GET
// /api/invoices/export, the invoice *register* (read access to issued
// invoices), not configuration — use "reports" instead, per independent
// review: #709 already gates this same feature's entry point (the
// journal's "🧾 Invoices" nav link, journal_page.go's InvoicingOn flag) on
// canPerform(d, r, "reports"), and gating the destination on "settings"
// would have silently contradicted that — inert today since both actions
// grant identically (manager/admin/super_admin), but a future role split
// (e.g. a bookkeeper granted "reports" without "settings") would hit a
// dead link at the button and then a live 403 if they typed the URL
// directly. "reports" also matches invoice_page.go's own audit-log
// category for the export ("invoice", not "settings" — the settings-audit
// rationale never actually applied to these two sites).
func newReceiptPrintInvoiceGateTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	initPagesI18n(t)
	isolatePluginsDir(t) // repoints paths.DataDir() at a throwaway temp dir
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db) // role_permissions must exist for AuthSvc.Can() to answer

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	return &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
		AuthSvc:  auth.NewService(db),
	}
}

// Positive counterpart to each subsystem's own pre-existing
// TestXxx_RequiresManager negative tests in receipt_designer_test.go,
// print_api_test.go and invoice_page_test.go (which already exercise the
// no-session short-circuit and pass unchanged post-#712, since canPerform's
// manager/admin grants exactly mirror isManagerOrAuthOff's manager/admin
// split). This table proves the REAL session path too (canPerform()/
// Auth.Can(), ut-docs#712): a real cashier session must still be denied by
// the actual permission check, and a real manager session must get PAST the
// auth gate. super_admin is included since that's the one accepted,
// documented broadening versus today's gate (#554/#555) -- nothing in
// production creates that role yet, so it's inert for every real till.
//
// Several of these endpoints then fail/redirect for unrelated reasons under
// this minimal fixture (no printer configured, blank form fields) -- this
// only asserts the response isn't the auth gate's own denial code, same
// scope as ut-docs#706's TestPluginManagementEndpoints_RealSessionGatesByRole.
func TestReceiptPrintInvoiceEndpoints_RealSessionGatesByRole(t *testing.T) {
	dp := newReceiptPrintInvoiceGateTestDeps(t)
	// The invoice register/export routes redirect to /settings when no
	// seller identity is configured yet -- same 303 status as the auth
	// gate's own redirect, so without this a "past the gate" manager
	// request would coincidentally land on the same status code as a
	// denied one. Configuring it isolates the auth check, same as the
	// pre-existing TestGetInvoices_RequiresManager does.
	setSeller(t, dp)

	mux := http.NewServeMux()
	registerReceiptDesigner(mux, dp)
	registerPrintAPI(mux, dp)
	registerInvoices(mux, dp)

	cases := []struct {
		name, method, path string
		denyCode           int // the gate's own refusal status; StatusForbidden unless noted
	}{
		{"receipt designer page", http.MethodGet, "/receipt-designer", http.StatusSeeOther},
		{"receipt designer preview", http.MethodPost, "/api/receipt-designer/preview", http.StatusForbidden},
		{"receipt designer save", http.MethodPost, "/api/receipt-designer/save", http.StatusForbidden},
		{"receipt designer logo", http.MethodPost, "/api/receipt-designer/logo", http.StatusForbidden},
		{"receipt designer test print", http.MethodPost, "/api/receipt-designer/test", http.StatusForbidden},
		{"printer settings", http.MethodPost, "/api/settings/printer", http.StatusForbidden},
		{"print test", http.MethodPost, "/api/print/test", http.StatusForbidden},
		{"invoices register page", http.MethodGet, "/invoices", http.StatusSeeOther},
		{"invoices export", http.MethodGet, "/api/invoices/export", http.StatusForbidden},
		// Must run LAST: its manager/admin/super_admin "past gate" requests
		// POST a blank form, which overwrites the seller identity setSeller
		// configured above with empty strings -- if this ran earlier, the
		// "invoices register page" case above would spuriously redirect
		// (sellerConfig off) instead of proving it got past the auth gate.
		// ("invoices export" is unaffected either way -- GET
		// /api/invoices/export has no sellerConfig check.) Independently
		// re-verified by review: reordering this case first does make the
		// register-page subtests fail as described, confirming the
		// ordering dependency is real, not just asserted in a comment.
		{"invoice seller settings", http.MethodPost, "/api/settings/invoice", http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/cashier_denied", func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, nil), auth.User{ID: "u1", Role: "cashier"})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.denyCode {
				t.Fatalf("%s %s cashier = %d, want %d", tc.method, tc.path, rec.Code, tc.denyCode)
			}
		})
		for _, role := range []string{"manager", "admin", "super_admin"} {
			t.Run(tc.name+"/"+role+"_past_gate", func(t *testing.T) {
				req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, nil), auth.User{ID: "u1", Role: role})
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code == tc.denyCode {
					t.Fatalf("%s %s %s = %d (the gate's own denial code), want past the auth gate (fixture has no printer/seller edge cases beyond setSeller, so a non-denyCode failure downstream is fine)", tc.method, tc.path, role, rec.Code)
				}
			})
		}
	}
}

// The one call site the card explicitly excludes: issuing an invoice for a
// completed sale has never been manager-gated (any cashier can invoice their
// own completed sale) and #712 must not add a gate here -- that would be
// scope-creeping behavior change beyond the isManagerOrAuthOff->canPerform
// swap. This locks the no-gate behavior in so a future pass doesn't
// accidentally sweep this route in with the rest of invoice_page.go.
func TestInvoicesIssue_StillHasNoManagerGate(t *testing.T) {
	dp := newReceiptPrintInvoiceGateTestDeps(t)
	setSeller(t, dp)
	mux := http.NewServeMux()
	registerInvoices(mux, dp)

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/invoices/issue", nil), auth.User{ID: "u1", Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("POST /api/invoices/issue cashier = 403, want no auth gate on this route (unchanged by #712)")
	}
}
