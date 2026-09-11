package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// --- ADR-0077 Decision 3/4: fiscal.sign.reconcile.ask + sweep (ut-docs#1520)

// seedFiscalSignHookPlugin plants the rows for an installed plugin holding
// ONE fiscal-signing hook event — seedFiscalSignPluginRows generalized to any
// event of the ADR-0077 D3 exclusivity group. INSERT OR IGNORE on the
// plugin/catalog/permission rows so the same plugin id can be given a second
// hook (the realistic shape: one signer holding ask + reconcile) without a
// PK collision; the hook row itself is always inserted.
func seedFiscalSignHookPlugin(t *testing.T, dp *common.Deps, pluginID, event string, active bool) {
	t.Helper()
	ctx := context.Background()
	activeInt := 0
	if active {
		activeInt = 1
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT OR IGNORE INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at)
VALUES (?, '1.0.0', ?, 'desc', 'wasm', './plugin.wasm', 'url', 'sha', 'auth', 'site', '[]', '0.0.0', '1', datetime('now'))`,
		pluginID, "Fiscal Sign "+pluginID); err != nil {
		t.Fatalf("seed plugin_catalog: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT OR IGNORE INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level)
VALUES (?, ?, '1.0.0', 'installed', './plugin.wasm', 'wasm', ?, 'trusted')`,
		pluginID, "Fiscal Sign "+pluginID, activeInt); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
VALUES (?, ?, ?, 'fiscal.sign', 1)`, "hook-"+event+"-"+pluginID, pluginID, event); err != nil {
		t.Fatalf("seed plugin_hooks: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT OR IGNORE INTO plugin_permissions (id, plugin_id, permission, granted)
VALUES (?, ?, 'events:receive', 1)`, "perm-"+pluginID, pluginID); err != nil {
		t.Fatalf("seed plugin_permissions: %v", err)
	}
}

// subscribeFiscalSignReconcileHandler registers an in-process Go handler as
// the fiscal.sign.reconcile.ask answerer — subscribeFiscalSignHandler's twin
// for the ADR-0077 D3 point.
func subscribeFiscalSignReconcileHandler(t *testing.T, dp *common.Deps, pluginID string, h plugins.EventHandler) {
	t.Helper()
	seedFiscalSignHookPlugin(t, dp, pluginID, fiscalSignReconcileAskEvent, true)
	bus := plugins.SharedBus(dp.Db)
	if _, err := bus.SubscribeWithHandler(context.Background(), pluginID, []string{fiscalSignReconcileAskEvent}, h); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
}

// declareFiscalGap writes the exact audit row declareUnsignedFiscalSale
// writes for the given outcome — the real writer, not a hand-rolled row, so
// the sweep's eligibility query is tested against the payload shape the
// tender path actually produces.
func declareFiscalGap(t *testing.T, dp *common.Deps, saleID string, outcome fiscalSignOutcome) {
	t.Helper()
	declareUnsignedFiscalSale(context.Background(), data.NewPOSRepo(dp.Db), saleID, "", fiscalSignResult{Outcome: outcome, Reason: "test: " + fmt.Sprint(outcome)})
}

// confirmedReconcileAnswer builds a {"status":"confirmed",...} answer with
// full evidence; txID may be "" (omitted), logTime is stamped as both
// start_time and log_time.
func confirmedReconcileAnswer(txID string, logTime time.Time) json.RawMessage {
	ts := logTime.UTC().Format(time.RFC3339)
	resp := map[string]any{
		"status":      "confirmed",
		"tx_revision": 2,
		"tse": map[string]any{
			"transaction_number":  4712,
			"signature_counter":   12346,
			"serial_number":       "TSE-TEST-SERIAL-1",
			"start_time":          ts,
			"log_time":            ts,
			"signature":           "RECONCILEDSIGBASE64==",
			"signature_algorithm": "ecdsa-plain-SHA256",
		},
	}
	if txID != "" {
		resp["tx_id"] = txID
	}
	raw, _ := json.Marshal(resp)
	return raw
}

// assertNothingReconciled asserts the full "write nothing" outcome: no
// reconciled evidence row, no fiscal_signing_reconciled marker, nothing in
// the receipt-facing fiscal_tse_signatures table, and — always — no
// pre-ADR-0056 fiscal_signing_resolved marker.
func assertNothingReconciled(t *testing.T, dp *common.Deps, saleID, why string) {
	t.Helper()
	repo := data.NewPOSRepo(dp.Db)
	if _, ok, err := repo.GetFiscalTSEReconciledSignature(context.Background(), saleID); ok || err != nil {
		t.Fatalf("%s: expected no reconciled evidence row for %s, ok=%v err=%v", why, saleID, ok, err)
	}
	if n := countAuditRowsFor(t, dp, saleID, fiscalSignGapActionReconciled); n != 0 {
		t.Fatalf("%s: expected no fiscal_signing_reconciled marker for %s, got %d", why, saleID, n)
	}
	assertReceiptTablesUntouched(t, dp, saleID, why)
}

// countAuditRowsFor is countAuditRows scoped to one sale.
func countAuditRowsFor(t *testing.T, dp *common.Deps, saleID, action string) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='sale' AND entity_id=? AND action=?`, saleID, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// assertReceiptTablesUntouched: the receipt render paths' own evidence table
// never gains a row from reconcile (ADR-0077 D4), and fiscal_signing_resolved
// is never written by any current code path (ADR-0056).
func assertReceiptTablesUntouched(t *testing.T, dp *common.Deps, saleID, why string) {
	t.Helper()
	repo := data.NewPOSRepo(dp.Db)
	if _, ok, err := repo.GetFiscalTSESignature(context.Background(), saleID); ok || err != nil {
		t.Fatalf("%s: reconcile must never write the receipt-facing fiscal_tse_signatures table (ADR-0077 D4), ok=%v err=%v", why, ok, err)
	}
	if n := countAuditRows(t, dp, "fiscal_signing_resolved"); n != 0 {
		t.Fatalf("%s: no code path may write fiscal_signing_resolved (ADR-0056), got %d rows", why, n)
	}
}

// --- §1 backend-vs-entry audit distinction ---------------------------------

// The unsigned_fiscal_signing payload now records WHICH outcome produced the
// gap — the prerequisite ADR-0077 D3 names for reconcile eligibility, since
// only a backend-level failure is ever a candidate. cannot-sign keeps its
// own action and gains no such field.
func TestFiscalSignDeclare_SigningPayloadRecordsOutcome(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	cases := []struct {
		sale    string
		outcome fiscalSignOutcome
		action  string
		want    string // expected "outcome" value; "" = key must be absent
	}{
		{"sale-backend", fiscalSignFailedBackend, fiscalSignGapActionSigning, "backend"},
		{"sale-entry", fiscalSignFailedEntry, fiscalSignGapActionSigning, "entry"},
		{"sale-offline", fiscalSignSkippedOffline, fiscalSignGapActionSigning, "offline"},
		{"sale-cannot", fiscalSignCannotSign, fiscalSignGapActionCannotSign, ""},
	}
	for _, c := range cases {
		declareFiscalGap(t, dp, c.sale, c.outcome)
		var payload string
		if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE entity_type='sale' AND entity_id=? AND action=?`, c.sale, c.action).Scan(&payload); err != nil {
			t.Fatalf("%s: expected a %s row: %v", c.sale, c.action, err)
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(payload), &fields); err != nil {
			t.Fatal(err)
		}
		got, present := fields["outcome"]
		if c.want == "" {
			if present {
				t.Fatalf("%s: the cannot-sign payload must not carry an outcome field, got %v", c.sale, got)
			}
			continue
		}
		if got != c.want {
			t.Fatalf("%s: outcome = %v, want %q (payload %s)", c.sale, got, c.want, payload)
		}
		// The pre-existing fields are unchanged.
		if _, ok := fields["known_offline"]; !ok {
			t.Fatalf("%s: known_offline must still be present", c.sale)
		}
		if _, ok := fields["failed_at"]; !ok {
			t.Fatalf("%s: failed_at must still be present", c.sale)
		}
	}
}

// --- §2 eligibility ---------------------------------------------------------

// Only a backend-level failure (budget expired / "unreachable") is ever
// dispatched to fiscal.sign.reconcile.ask. entry-level, cannot-sign and
// known-offline gaps are never asked; an already-reconciled sale is never
// re-asked; a gap older than the lookback window is never asked.
func TestFiscalSignReconcile_OnlyBackendFailuresAreEligible(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)

	declareFiscalGap(t, dp, "sale-backend", fiscalSignFailedBackend)
	declareFiscalGap(t, dp, "sale-entry", fiscalSignFailedEntry)
	declareFiscalGap(t, dp, "sale-cannot", fiscalSignCannotSign)
	declareFiscalGap(t, dp, "sale-offline", fiscalSignSkippedOffline)
	// Already reconciled: the marker row alone must exclude it.
	declareFiscalGap(t, dp, "sale-done", fiscalSignFailedBackend)
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.InsertAudit(ctx, nil, "system", "sale", "sale-done", fiscalSignGapActionReconciled, map[string]any{"check_tier": "exact"}, now, ""); err != nil {
		t.Fatal(err)
	}
	// Older than the lookback window (a backend gap in every other respect).
	old := time.Now().UTC().Add(-fiscalSignReconcileLookback - time.Hour).Format(time.RFC3339)
	if err := repo.InsertAudit(ctx, nil, "", "sale", "sale-stale", fiscalSignGapActionSigning, map[string]any{
		"reason": "old", "known_offline": false, "failed_at": old, "outcome": "backend",
	}, old, ""); err != nil {
		t.Fatal(err)
	}

	var asked []string
	subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-elig", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p fiscalSignReconcileAskPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		asked = append(asked, p.SaleID)
		return json.RawMessage(`{"status":"not-found"}`), nil
	})

	fiscalSignReconcileTick(ctx, dp)

	if len(asked) != 1 || asked[0] != "sale-backend" {
		t.Fatalf("exactly the backend-failure sale must be asked, got %v", asked)
	}
	// not-found writes nothing, so the same sale is re-considered next tick
	// (no per-sale tried-and-failed state — ADR-0077 D3 "plain, boring").
	fiscalSignReconcileTick(ctx, dp)
	if len(asked) != 2 || asked[1] != "sale-backend" {
		t.Fatalf("a not-found sale stays eligible for the next tick, got %v", asked)
	}
	for _, s := range []string{"sale-backend", "sale-entry", "sale-cannot", "sale-offline", "sale-stale"} {
		assertNothingReconciled(t, dp, s, "not-found answer")
	}
}

// The request carries whatever identifier core actually holds (ADR-0077 D3):
// started_tx_id/started_tx_revision when the D1 round trip captured one,
// omitted entirely — sale_id alone — when it didn't.
func TestFiscalSignReconcile_RequestCarriesHeldIdentifierOnly(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	declareFiscalGap(t, dp, "sale-with-tx", fiscalSignFailedBackend)
	declareFiscalGap(t, dp, "sale-without-tx", fiscalSignFailedBackend)
	if err := repo.RecordFiscalSignStart(ctx, "sale-with-tx", "tx-held-1", 1); err != nil {
		t.Fatal(err)
	}
	raw := map[string]string{}
	subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-req", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p struct {
			SaleID string `json:"sale_id"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		raw[p.SaleID] = string(ev.Payload)
		return json.RawMessage(`{"status":"not-found"}`), nil
	})
	fiscalSignReconcileTick(ctx, dp)
	if got := raw["sale-with-tx"]; !strings.Contains(got, `"started_tx_id":"tx-held-1"`) || !strings.Contains(got, `"started_tx_revision":1`) {
		t.Fatalf("expected the held identifier echoed in the request, got %s", got)
	}
	if got := raw["sale-without-tx"]; strings.Contains(got, "started_tx") {
		t.Fatalf("a sale with no captured identifier must be asked with sale_id alone, got %s", got)
	}
}

// --- §2 two-tier check --------------------------------------------------------

// Tier "exact": core holds a tx_id → the answer's tx_id must equal it. On a
// match, the evidence is persisted (in the reconcile-only table) and the
// fiscal_signing_reconciled marker is written — attributed to the "system"
// actor like the unattended EOD tick, recording the tier and identifier.
func TestFiscalSignReconcile_ExactTxIDMatchConfirms(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	declareFiscalGap(t, dp, "sale-exact", fiscalSignFailedBackend)
	if err := repo.RecordFiscalSignStart(ctx, "sale-exact", "tx-exact-1", 1); err != nil {
		t.Fatal(err)
	}
	// Evidence timestamps deliberately FAR outside the degraded window: the
	// exact tier is the stronger check and does not fall back to the time
	// window at all.
	subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-exact", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return confirmedReconcileAnswer("tx-exact-1", time.Now().Add(-30*24*time.Hour)), nil
	})

	fiscalSignReconcileTick(ctx, dp)

	rec, ok, err := repo.GetFiscalTSEReconciledSignature(ctx, "sale-exact")
	if err != nil || !ok {
		t.Fatalf("expected a reconciled evidence row, ok=%v err=%v", ok, err)
	}
	if rec.TxID != "tx-exact-1" || rec.CheckTier != fiscalSignReconcileTierExact ||
		rec.Signature != "RECONCILEDSIGBASE64==" || rec.TransactionNumber != 4712 ||
		rec.SignatureCounter != 12346 || rec.SerialNumber != "TSE-TEST-SERIAL-1" ||
		rec.SignatureAlgorithm != "ecdsa-plain-SHA256" {
		t.Fatalf("reconciled evidence mismatch: %+v", *rec)
	}
	var actor, payload string
	if err := dp.Db.QueryRow(`SELECT COALESCE(actor_id,''), data_json FROM audit_log WHERE entity_type='sale' AND entity_id='sale-exact' AND action=?`, fiscalSignGapActionReconciled).Scan(&actor, &payload); err != nil {
		t.Fatalf("expected a fiscal_signing_reconciled marker: %v", err)
	}
	if actor != "system" {
		t.Fatalf("background reconcile must be attributed to the system actor, got %q", actor)
	}
	if !strings.Contains(payload, `"check_tier":"exact"`) || !strings.Contains(payload, `"tx_id":"tx-exact-1"`) || !strings.Contains(payload, `"reconciled_at"`) {
		t.Fatalf("marker payload must record the tier, identifier and time, got %s", payload)
	}
	// The gap itself stays: the original unsigned marker is never removed
	// or amended (the sale's permanent tender-time record).
	if n := countAuditRows(t, dp, fiscalSignGapActionSigning); n != 1 {
		t.Fatalf("the original unsigned_fiscal_signing marker must remain, got %d", n)
	}
	assertReceiptTablesUntouched(t, dp, "sale-exact", "exact-tier confirm")
}

// Tier "exact", mismatch: the answer names a different transaction than the
// one core supplied → discarded, nothing written.
func TestFiscalSignReconcile_MismatchedTxIDWritesNothing(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	declareFiscalGap(t, dp, "sale-mismatch", fiscalSignFailedBackend)
	if err := repo.RecordFiscalSignStart(ctx, "sale-mismatch", "tx-real", 1); err != nil {
		t.Fatal(err)
	}
	for name, txID := range map[string]string{"different id": "tx-other", "omitted id": ""} {
		t.Run(name, func(t *testing.T) {
			plugins.SharedBus(dp.Db).ResetSubscribers()
			subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-mismatch-"+strings.ReplaceAll(name, " ", "-"), func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
				// In-window timestamps: the mismatch must NOT be rescued by
				// the degraded tier — when core holds a tx_id, tx_id decides.
				return confirmedReconcileAnswer(txID, time.Now()), nil
			})
			fiscalSignReconcileTick(ctx, dp)
			assertNothingReconciled(t, dp, "sale-mismatch", "tx_id mismatch")
		})
	}
}

// Tier "window" (degraded, no tx_id held): the evidence's start/log time must
// fall within fiscalSignReconcileWindow of the sale's failed_at.
func TestFiscalSignReconcile_NoTxIDInWindowConfirms(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	declareFiscalGap(t, dp, "sale-window", fiscalSignFailedBackend)
	subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-window", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return confirmedReconcileAnswer("tx-whatever", time.Now().Add(-fiscalSignReconcileWindow/2)), nil
	})
	fiscalSignReconcileTick(ctx, dp)
	rec, ok, err := repo.GetFiscalTSEReconciledSignature(ctx, "sale-window")
	if err != nil || !ok {
		t.Fatalf("expected a reconciled evidence row, ok=%v err=%v", ok, err)
	}
	if rec.CheckTier != fiscalSignReconcileTierWindow || rec.TxID != "" {
		t.Fatalf("degraded tier must be recorded as such with no tx_id, got %+v", *rec)
	}
	var payload string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE entity_type='sale' AND entity_id='sale-window' AND action=?`, fiscalSignGapActionReconciled).Scan(&payload); err != nil {
		t.Fatalf("expected a fiscal_signing_reconciled marker: %v", err)
	}
	if !strings.Contains(payload, `"check_tier":"window"`) {
		t.Fatalf("marker must record the degraded tier, got %s", payload)
	}
	assertReceiptTablesUntouched(t, dp, "sale-window", "window-tier confirm")
}

// Tier "window", outside the window (or with no usable timestamp at all):
// nothing written.
func TestFiscalSignReconcile_NoTxIDOutOfWindowWritesNothing(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	declareFiscalGap(t, dp, "sale-outside", fiscalSignFailedBackend)
	answers := map[string]json.RawMessage{
		"too early":      confirmedReconcileAnswer("", time.Now().Add(-fiscalSignReconcileWindow-time.Minute)),
		"too late":       confirmedReconcileAnswer("", time.Now().Add(fiscalSignReconcileWindow+time.Minute)),
		"no timestamps":  json.RawMessage(`{"status":"confirmed","tse":{"signature":"SIG=="}}`),
		"bad timestamps": json.RawMessage(`{"status":"confirmed","tse":{"signature":"SIG==","log_time":"yesterday"}}`),
	}
	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			plugins.SharedBus(dp.Db).ResetSubscribers()
			subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-out-"+strings.ReplaceAll(name, " ", "-"), func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
				return answer, nil
			})
			fiscalSignReconcileTick(ctx, dp)
			assertNothingReconciled(t, dp, "sale-outside", name)
		})
	}
}

// The pure check, table-driven, so every branch is pinned independently of
// the sweep plumbing.
func TestFiscalSignReconcileCheck(t *testing.T) {
	failedAt := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	anchor := failedAt.Format(time.RFC3339)
	ev := func(start, logT string) *fiscalTSEEvidence {
		return &fiscalTSEEvidence{Signature: "SIG==", StartTime: start, LogTime: logT}
	}
	in := failedAt.Add(fiscalSignReconcileWindow - time.Second).Format(time.RFC3339)
	out := failedAt.Add(fiscalSignReconcileWindow + time.Second).Format(time.RFC3339)
	cases := []struct {
		name     string
		held     string
		failedAt string
		resp     fiscalSignReconcileAskResponse
		wantTier string
		wantOK   bool
	}{
		{"exact match", "tx-1", anchor, fiscalSignReconcileAskResponse{TxID: "tx-1", TSE: ev(out, out)}, fiscalSignReconcileTierExact, true},
		{"exact mismatch", "tx-1", anchor, fiscalSignReconcileAskResponse{TxID: "tx-2", TSE: ev(in, in)}, "", false},
		{"exact missing", "tx-1", anchor, fiscalSignReconcileAskResponse{TSE: ev(in, in)}, "", false},
		{"window both in", "", anchor, fiscalSignReconcileAskResponse{TxID: "tx-9", TSE: ev(in, in)}, fiscalSignReconcileTierWindow, true},
		{"window log only in", "", anchor, fiscalSignReconcileAskResponse{TSE: ev("", in)}, fiscalSignReconcileTierWindow, true},
		{"window start out", "", anchor, fiscalSignReconcileAskResponse{TSE: ev(out, in)}, "", false},
		{"window log out", "", anchor, fiscalSignReconcileAskResponse{TSE: ev(in, out)}, "", false},
		{"window none", "", anchor, fiscalSignReconcileAskResponse{TSE: ev("", "")}, "", false},
		{"window unparseable", "", anchor, fiscalSignReconcileAskResponse{TSE: ev("soon", "")}, "", false},
		{"window bad anchor", "", "not-a-time", fiscalSignReconcileAskResponse{TSE: ev(in, in)}, "", false},
		{"nil evidence", "", anchor, fiscalSignReconcileAskResponse{}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tier, ok := fiscalSignReconcileCheck(c.held, c.failedAt, c.resp)
			if ok != c.wantOK || tier != c.wantTier {
				t.Fatalf("got (%q, %v), want (%q, %v)", tier, ok, c.wantTier, c.wantOK)
			}
		})
	}
}

// --- §2 response handling -----------------------------------------------------

// A "confirmed" whose evidence lacks the signature itself (hasSignature
// false) proves nothing — nothing written, in either tier.
func TestFiscalSignReconcile_ConfirmedWithoutSignatureWritesNothing(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	declareFiscalGap(t, dp, "sale-nosig-exact", fiscalSignFailedBackend)
	declareFiscalGap(t, dp, "sale-nosig-window", fiscalSignFailedBackend)
	if err := repo.RecordFiscalSignStart(ctx, "sale-nosig-exact", "tx-nosig", 1); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-nosig", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"confirmed","tx_id":"tx-nosig","tse":{"transaction_number":1,"serial_number":"S","start_time":"` + now + `","log_time":"` + now + `"}}`), nil
	})
	fiscalSignReconcileTick(ctx, dp)
	assertNothingReconciled(t, dp, "sale-nosig-exact", "confirmed without signature (exact tier)")
	assertNothingReconciled(t, dp, "sale-nosig-window", "confirmed without signature (window tier)")
}

// Anything that is not exactly "confirmed" — "not-found", an unknown status,
// unparseable JSON, no answer — writes nothing. The wire shape has no third
// state that could mean "sign it now" (ADR-0077 D3).
func TestFiscalSignReconcile_NonConfirmedAnswersWriteNothing(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	declareFiscalGap(t, dp, "sale-nc", fiscalSignFailedBackend)
	answers := map[string]json.RawMessage{
		"not-found":      json.RawMessage(`{"status":"not-found"}`),
		"approved":       json.RawMessage(`{"status":"approved","tse":{"signature":"SIG=="}}`),
		"unknown status": json.RawMessage(`{"status":"sign-now","tse":{"signature":"SIG=="}}`),
		"garbage":        json.RawMessage(`{not json`),
		"declined":       nil,
	}
	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			plugins.SharedBus(dp.Db).ResetSubscribers()
			subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-nc-"+strings.ReplaceAll(name, " ", "-"), func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
				return answer, nil
			})
			fiscalSignReconcileTick(ctx, dp)
			assertNothingReconciled(t, dp, "sale-nc", name)
		})
	}
}

// A handler error (a wasm trap, a transport failure) ends the pass quietly:
// nothing written, no panic, and the sale is simply re-considered next tick.
// A handler PANIC is contained by the tick's recover() — a background
// goroutine must never take down the till process mid-sale.
func TestFiscalSignReconcile_HandlerErrorOrPanicIsContained(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	declareFiscalGap(t, dp, "sale-err", fiscalSignFailedBackend)
	var calls atomic.Int32
	subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-err", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		if calls.Add(1) == 1 {
			return nil, fmt.Errorf("simulated transport failure")
		}
		panic("simulated guest panic")
	})
	fiscalSignReconcileTick(ctx, dp) // error
	fiscalSignReconcileTick(ctx, dp) // panic — must not escape
	if calls.Load() != 2 {
		t.Fatalf("expected the sale to be re-considered on the next tick, got %d calls", calls.Load())
	}
	assertNothingReconciled(t, dp, "sale-err", "handler error/panic")
}

// Idempotent by construction: a confirmed sale is written exactly once and
// never asked again; a second confirmation (e.g. two ticks racing a slow
// marker write) cannot duplicate or overwrite the first evidence.
func TestFiscalSignReconcile_ConfirmedOnceNeverReasked(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	declareFiscalGap(t, dp, "sale-once", fiscalSignFailedBackend)
	var calls atomic.Int32
	subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-once", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		calls.Add(1)
		return confirmedReconcileAnswer("", time.Now()), nil
	})
	fiscalSignReconcileTick(ctx, dp)
	fiscalSignReconcileTick(ctx, dp)
	if calls.Load() != 1 {
		t.Fatalf("a reconciled sale must never be asked again, got %d calls", calls.Load())
	}
	// Repository-level idempotence: a second record for the same sale is a
	// no-op (first write wins), same as RecordFiscalTSESignature.
	if err := repo.RecordFiscalTSEReconciledSignature(ctx, data.FiscalTSEReconciledSignature{SaleID: "sale-once", CheckTier: "exact", TxID: "tx-late", Signature: "OTHER=="}); err != nil {
		t.Fatal(err)
	}
	rec, _, _ := repo.GetFiscalTSEReconciledSignature(ctx, "sale-once")
	if rec.Signature != "RECONCILEDSIGBASE64==" || rec.CheckTier != fiscalSignReconcileTierWindow {
		t.Fatalf("first recorded evidence must never be overwritten, got %+v", *rec)
	}
	if n := countAuditRows(t, dp, fiscalSignGapActionReconciled); n != 1 {
		t.Fatalf("expected exactly one fiscal_signing_reconciled marker, got %d", n)
	}
}

// --- D4: receipts are byte-identical before and after reconcile -------------

// renderHTMLReceiptForSale renders the inline HTML receipt for a persisted
// sale exactly as pos_api.go's tender response derives its fiscal inputs:
// the gap notice from saleFiscalSigningGapKind, the TSE block from
// fiscal_tse_signatures, the device block from fiscal_device_receipts.
func renderHTMLReceiptForSale(t *testing.T, dp *common.Deps, saleID string) string {
	t.Helper()
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("€%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T":          func(key string) string { return key },
	}
	gap := saleFiscalSigningGapKind(ctx, repo, saleID)
	tse, _, _ := repo.GetFiscalTSESignature(ctx, saleID)
	dev, _, _ := repo.GetFiscalDeviceReceipt(ctx, saleID)
	lines := []pos.SaleLineInput{{Name: "Apple", Qty: 1, UnitPrice: 120}}
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 120}}
	html, err := renderReceipt(funcs, "R-D4", lines, payments, 120, 0, 120, false, 0, "", 0, nil, false, false,
		gap == fiscalSignGapActionSigning, gap == fiscalSignGapActionCannotSign, tse, dev, "Shop", receiptDesign{ShowTax: true}, "", nil, "")
	if err != nil {
		t.Fatalf("renderReceipt: %v", err)
	}
	return html
}

// ADR-0077 D4, the hard constraint on D3: a confirmed reconcile changes
// NOTHING on either receipt render path — the tender-time outage notice
// stays, and no TSE block appears. Asserted as byte identity of the HTML
// receipt and the ESC/POS document before vs. after the reconcile write,
// plus the notice's continued presence (so "identical" isn't trivially
// "identically empty").
func TestFiscalSignReconcile_ReceiptsAreByteIdenticalAfterReconcile(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	seedReceiptSale(t, dp, "sale-d4", "R-D4", "sale", "", 120, 0, 0)
	declareFiscalGap(t, dp, "sale-d4", fiscalSignFailedBackend)
	if err := repo.RecordFiscalSignStart(ctx, "sale-d4", "tx-d4", 1); err != nil {
		t.Fatal(err)
	}

	htmlBefore := renderHTMLReceiptForSale(t, dp, "sale-d4")
	docBefore, err := buildReceiptDoc(ctx, dp, "R-D4")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(htmlBefore, "receipt.fiscal.unsigned_signing") {
		t.Fatalf("precondition: the HTML receipt must carry the outage notice before reconcile: %s", htmlBefore)
	}
	if !strings.Contains(strings.Join(docBefore.Meta, "\n"), "TSE unreachable") {
		t.Fatalf("precondition: the ESC/POS receipt must carry the outage notice before reconcile: %v", docBefore.Meta)
	}

	subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-d4", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return confirmedReconcileAnswer("tx-d4", time.Now()), nil
	})
	fiscalSignReconcileTick(ctx, dp)
	if _, ok, _ := repo.GetFiscalTSEReconciledSignature(ctx, "sale-d4"); !ok {
		t.Fatal("precondition: the reconcile must actually have been confirmed and recorded")
	}
	if n := countAuditRows(t, dp, fiscalSignGapActionReconciled); n != 1 {
		t.Fatalf("precondition: expected the fiscal_signing_reconciled marker, got %d", n)
	}

	htmlAfter := renderHTMLReceiptForSale(t, dp, "sale-d4")
	docAfter, err := buildReceiptDoc(ctx, dp, "R-D4")
	if err != nil {
		t.Fatal(err)
	}
	if htmlAfter != htmlBefore {
		t.Fatalf("ADR-0077 D4: the HTML receipt changed after reconcile\nbefore:\n%s\nafter:\n%s", htmlBefore, htmlAfter)
	}
	before, _ := json.Marshal(docBefore)
	after, _ := json.Marshal(docAfter)
	if string(before) != string(after) {
		t.Fatalf("ADR-0077 D4: the ESC/POS receipt changed after reconcile\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if strings.Contains(htmlAfter, "RECONCILEDSIGBASE64==") || strings.Contains(string(after), "RECONCILEDSIGBASE64==") {
		t.Fatal("ADR-0077 D4: reconciled evidence must never reach a receipt")
	}
	if saleFiscalSigningGapKind(ctx, repo, "sale-d4") != fiscalSignGapActionSigning {
		t.Fatal("ADR-0077 D4: saleFiscalSigningGapKind must be completely unaffected by fiscal_signing_reconciled")
	}
}

// --- §3 sweep -----------------------------------------------------------------

// Zero-plugin cost: a tick with no fiscal.sign.reconcile.ask subscriber does
// no DB work at all — the candidate query is never even reached.
func TestFiscalSignReconcile_NoSubscriberDoesNoDBWork(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	declareFiscalGap(t, dp, "sale-idle", fiscalSignFailedBackend)
	orig := fiscalSignReconcileCandidatesFn
	t.Cleanup(func() { fiscalSignReconcileCandidatesFn = orig })
	fiscalSignReconcileCandidatesFn = func(ctx context.Context, repo *data.POSRepo, since time.Time, afterRowID int64, limit int) ([]data.FiscalSignReconcileCandidate, error) {
		t.Fatal("a tick with no subscriber must not query the DB")
		return nil, nil
	}
	fiscalSignReconcileTick(context.Background(), dp)
	// And a subscriber to a SIBLING event (fiscal.sign.ask alone — the
	// entire installed base until ut-docs#1521) is not a reconcile
	// subscriber: still no DB work.
	subscribeFiscalSignHandler(t, dp, "com.test.ask-only", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	fiscalSignReconcileTick(context.Background(), dp)
}

// The registered sweep goroutine: honors ctx cancellation during its initial
// delay (no tick ever runs) and releases the WaitGroup — the shape every
// StartX background job in init.go shares.
func TestStartFiscalSignReconcileSweep_StopsOnContextCancel(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	orig := fiscalSignReconcileCandidatesFn
	t.Cleanup(func() { fiscalSignReconcileCandidatesFn = orig })
	subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-sweep", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"not-found"}`), nil
	})
	var ticks atomic.Int32
	fiscalSignReconcileCandidatesFn = func(ctx context.Context, repo *data.POSRepo, since time.Time, afterRowID int64, limit int) ([]data.FiscalSignReconcileCandidate, error) {
		ticks.Add(1)
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	StartFiscalSignReconcileSweep(ctx, dp, &wg)
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sweep goroutine did not exit on context cancellation")
	}
	if ticks.Load() != 0 {
		t.Fatalf("no tick may run before the initial delay elapses, got %d", ticks.Load())
	}
}

// Review finding ut-docs#1520 #1 (candidate starvation): a tick must walk
// PAST a permanently-not-found head, not just fetch the same fixed-size
// oldest slice forever. Seed more sales than fiscalSignReconcileBatchLimit,
// every one answering "not-found" (a genuine, permanent non-write outcome
// per D3) except the LAST one in the ordering, which confirms. Before the
// pagination fix this test's own production code returned only the first
// page and the last sale was never even asked; after it, one tick pages
// through the whole eligible set and reaches it.
func TestFiscalSignReconcile_TickPagesPastAPermanentlyUnresolvedHead(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	total := fiscalSignReconcileBatchLimit + 5
	var confirmSaleID string
	for i := 0; i < total; i++ {
		saleID := fmt.Sprintf("sale-page-%03d", i)
		declareFiscalGap(t, dp, saleID, fiscalSignFailedBackend)
		if err := repo.RecordFiscalSignStart(ctx, saleID, fmt.Sprintf("tx-page-%03d", i), 1); err != nil {
			t.Fatal(err)
		}
		if i == total-1 {
			confirmSaleID = saleID
		}
	}
	var asked []string
	subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-page", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		var req fiscalSignReconcileAskPayload
		if err := json.Unmarshal(ev.Payload, &req); err != nil {
			t.Fatal(err)
		}
		asked = append(asked, req.SaleID)
		if req.SaleID == confirmSaleID {
			return confirmedReconcileAnswer(req.StartedTxID, time.Now()), nil
		}
		return json.RawMessage(`{"status":"not-found"}`), nil
	})
	fiscalSignReconcileTick(ctx, dp)
	if len(asked) != total {
		t.Fatalf("expected all %d eligible sales asked in one tick, got %d: %v", total, len(asked), asked)
	}
	if _, ok, _ := repo.GetFiscalTSEReconciledSignature(ctx, confirmSaleID); !ok {
		t.Fatalf("the last sale in the ordering (%s) must have been reached and reconciled within the same tick", confirmSaleID)
	}
	for i := 0; i < total-1; i++ {
		assertNothingReconciled(t, dp, fmt.Sprintf("sale-page-%03d", i), "every not-found sale ahead of the confirmed one")
	}
}

// Review finding ut-docs#1520 #2 (poison-pill sale): a per-sale handler
// error (the signer traps on ONE sale's specific payload — not a budget
// timeout) must not abort the rest of the pass. Seed three eligible sales;
// the middle one's handler call returns an error, the other two confirm.
func TestFiscalSignReconcile_PoisonPillSaleDoesNotBlockOthers(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	for _, id := range []string{"sale-pp-1", "sale-pp-2-poison", "sale-pp-3"} {
		declareFiscalGap(t, dp, id, fiscalSignFailedBackend)
		if err := repo.RecordFiscalSignStart(ctx, id, "tx-"+id, 1); err != nil {
			t.Fatal(err)
		}
	}
	subscribeFiscalSignReconcileHandler(t, dp, "com.test.reconcile-poison", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		var req fiscalSignReconcileAskPayload
		if err := json.Unmarshal(ev.Payload, &req); err != nil {
			t.Fatal(err)
		}
		if req.SaleID == "sale-pp-2-poison" {
			return nil, fmt.Errorf("simulated guest trap on this sale's payload")
		}
		return confirmedReconcileAnswer(req.StartedTxID, time.Now()), nil
	})
	fiscalSignReconcileTick(ctx, dp)
	if _, ok, _ := repo.GetFiscalTSEReconciledSignature(ctx, "sale-pp-1"); !ok {
		t.Fatal("sale-pp-1 (before the poison sale) must have been reconciled")
	}
	if _, ok, _ := repo.GetFiscalTSEReconciledSignature(ctx, "sale-pp-3"); !ok {
		t.Fatal("sale-pp-3 (after the poison sale) must still have been reached and reconciled — a per-sale error must not abort the pass")
	}
	assertNothingReconciled(t, dp, "sale-pp-2-poison", "the poison sale itself never confirms")
}

// End-to-end through the REAL wazero runtime: a wasm signer subscribed to
// fiscal.sign.reconcile.ask is dispatched blocking/value-returning purely by
// virtue of the ".ask" suffix (no runtime dispatch-mode change anywhere),
// echoes the held tx_id, and core records the exact-tier reconcile.
func TestFiscalSignReconcile_WasmGuestEndToEnd(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	const pluginID = "com.test.fiscal-reconcile-wasm"
	seedFiscalSignHookPlugin(t, dp, pluginID, fiscalSignReconcileAskEvent, true)
	guest := buildFiscalGuest(t, "fiscalsign_reconcile_guest")
	dir := filepath.Join(paths.Plugins(), pluginID, "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), guest, 0o644); err != nil {
		t.Fatal(err)
	}
	dp.Pm.Wasm.Sync(ctx, dp.Db)
	if !plugins.SharedBus(dp.Db).HasSubscribers(fiscalSignReconcileAskEvent) {
		t.Fatal("precondition: the wasm guest must be subscribed to fiscal.sign.reconcile.ask")
	}

	declareFiscalGap(t, dp, "sale-wasm-exact", fiscalSignFailedBackend)
	if err := repo.RecordFiscalSignStart(ctx, "sale-wasm-exact", "tx-wasm-1", 1); err != nil {
		t.Fatal(err)
	}
	// No held identifier → the guest answers not-found → nothing written.
	declareFiscalGap(t, dp, "sale-wasm-none", fiscalSignFailedBackend)

	fiscalSignReconcileTick(ctx, dp)

	rec, ok, err := repo.GetFiscalTSEReconciledSignature(ctx, "sale-wasm-exact")
	if err != nil || !ok {
		t.Fatalf("expected the wasm-confirmed reconcile to be recorded, ok=%v err=%v", ok, err)
	}
	if rec.TxID != "tx-wasm-1" || rec.CheckTier != fiscalSignReconcileTierExact || rec.Signature != "RECONCILEDSIGBASE64==" {
		t.Fatalf("unexpected reconciled evidence: %+v", *rec)
	}
	assertReceiptTablesUntouched(t, dp, "sale-wasm-exact", "wasm exact confirm")
	assertNothingReconciled(t, dp, "sale-wasm-none", "wasm not-found")
}

// The new reconcile code must never write (or even name) the pre-ADR-0056
// fiscal_signing_resolved marker — asserted at the source level, since the
// runtime tests above can only prove it for the paths they drive.
func TestFiscalSignReconcile_SourceNeverNamesResolvedMarker(t *testing.T) {
	// Located via the caller's own path, not the cwd: sibling tests chdir
	// to the repo root (chdirRoot), so a relative read is cwd-dependent.
	_, thisFile, _, _ := runtime.Caller(0)
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "fiscal_sign_reconcile.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "fiscal_signing_resolved") {
		t.Fatal("fiscal_sign_reconcile.go must never reference fiscal_signing_resolved (ADR-0056/ADR-0077 D3)")
	}
	if !strings.Contains(string(src), `"fiscal_signing_reconciled"`) {
		t.Fatal("fiscal_sign_reconcile.go must define the fiscal_signing_reconciled marker")
	}
}

// --- §4 enable-time exclusivity group ------------------------------------------

// Every directional pair across the three-event group at ENABLE time: a
// plugin already holding any one event blocks a different plugin declaring
// any one, with a 409 naming the owner; the refused plugin stays inactive.
func TestFiscalSignExclusivity_EnableRefusesEveryDirectionalPair(t *testing.T) {
	group := []string{fiscalSignAskEvent, fiscalSignStartEvent, fiscalSignReconcileAskEvent}
	for _, held := range group {
		for _, declared := range group {
			t.Run(held+" blocks "+declared, func(t *testing.T) {
				_, dp := newFiscalSignDeps(t)
				mux := http.NewServeMux()
				registerPluginAPI(mux, dp)
				seedFiscalSignHookPlugin(t, dp, "com.test.owner", held, true)
				seedFiscalSignHookPlugin(t, dp, "com.test.second", declared, false)

				req := httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.second/enable", nil)
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code != http.StatusConflict {
					t.Fatalf("expected 409 enabling a %s declarer while %s is held, got %d: %s", declared, held, rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), "com.test.owner") || !strings.Contains(rec.Body.String(), declared) {
					t.Fatalf("refusal must name the owning plugin and the declared point, got: %s", rec.Body.String())
				}
				var active int
				if err := dp.Db.QueryRow(`SELECT is_active FROM plugins WHERE id='com.test.second'`).Scan(&active); err != nil {
					t.Fatal(err)
				}
				if active != 0 {
					t.Fatal("the refused plugin must stay inactive")
				}
			})
		}
	}
}

// Self never conflicts at enable time either: re-enabling the sole owner
// (holding all three events) succeeds, and so does enabling a start-only or
// reconcile-only plugin when nobody else holds anything.
func TestFiscalSignExclusivity_EnableSelfAndUnownedGroupSucceed(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	for _, ev := range []string{fiscalSignAskEvent, fiscalSignStartEvent, fiscalSignReconcileAskEvent} {
		seedFiscalSignHookPlugin(t, dp, "com.test.full-signer", ev, false)
	}
	seedFiscalSignHookPlugin(t, dp, "com.test.start-only", fiscalSignStartEvent, false)
	seedFiscalSignHookPlugin(t, dp, "com.test.reconcile-only", fiscalSignReconcileAskEvent, false)

	enable := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/"+id+"/enable", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	// Nobody active: a start-only plugin enables fine.
	if rec := enable("com.test.start-only"); rec.Code != http.StatusOK {
		t.Fatalf("start-only plugin with no active owner must enable, got %d: %s", rec.Code, rec.Body.String())
	}
	// Now the group is owned by start-only → the full signer is refused…
	if rec := enable("com.test.full-signer"); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for the full signer while start-only owns the group, got %d", rec.Code)
	}
	// …and re-enabling the owner itself is never a self-conflict.
	if rec := enable("com.test.start-only"); rec.Code != http.StatusOK {
		t.Fatalf("re-enabling the owner must not conflict with itself, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := dp.Db.Exec(`UPDATE plugins SET is_active = 0 WHERE id = 'com.test.start-only'`); err != nil {
		t.Fatal(err)
	}
	if rec := enable("com.test.reconcile-only"); rec.Code != http.StatusOK {
		t.Fatalf("reconcile-only plugin with no active owner must enable, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Fail closed at enable time for the new events too.
func TestFiscalSignExclusivity_EnableFailsClosedForGroupEvents(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	seedFiscalSignHookPlugin(t, dp, "com.test.owner", fiscalSignAskEvent, true)
	seedFiscalSignHookPlugin(t, dp, "com.test.second", fiscalSignReconcileAskEvent, false)
	if _, err := dp.Db.Exec(`DROP TABLE plugin_hooks`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.second/enable", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a DB error during the group exclusivity check must refuse the enable, got %d: %s", rec.Code, rec.Body.String())
	}
	var active int
	if err := dp.Db.QueryRow(`SELECT is_active FROM plugins WHERE id='com.test.second'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("the plugin must stay inactive when the check could not run")
	}
}
