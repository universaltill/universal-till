package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// fiscal_sign_reconcile.go — ADR-0077 Decision 3 (ut-docs#1520): the
// fiscal.sign.reconcile.ask recovery path and the periodic sweep that
// drives it. NEW code, deliberately: this is NOT a rewire of the
// StartFiscalSignRetry/fiscalSignRetryTick machinery ADR-0056 deleted
// outright. That machinery could CREATE a signature (it re-ran the same live
// askFiscalSign a tender uses); nothing here can. The answer shape below has
// exactly two states — "not-found" and "confirmed" — and "confirmed" is
// checked against what core already holds before anything is written. A
// sale that was signed at tender time is never touched; a sale that failed
// for any reason other than a backend-level outage is never even asked (see
// data.POSRepo.ListFiscalSignReconcileCandidates for the exact set).
//
// What a confirmed reconcile changes — and what it never changes (D4):
//
//   - it records the §6 KassenSichV evidence the exclusive, verified signer
//     retrieved (fiscal_tse_reconciled_signatures — its OWN table, never the
//     receipt-facing fiscal_tse_signatures) and writes the
//     fiscal_signing_reconciled audit marker: an auditor-facing record for
//     the audit trail / a future DSFinV-K export;
//   - it never changes what any receipt render shows, original or reprint.
//     saleFiscalSigningGapKind is untouched by the new marker, so the
//     tender-time outage notice stays on every reprint, forever — fiskaly's
//     own clarification is that retrieving a signature later "does not mean
//     altering or reprinting the original receipt".

// fiscalSignReconcileAskEvent aliases plugins.FiscalSignReconcileAskEvent —
// same reason fiscalSignAskEvent aliases its own: the canonical constant
// lives where manifest persistence enforces the group's exclusivity.
const fiscalSignReconcileAskEvent = plugins.FiscalSignReconcileAskEvent

// fiscalSignGapActionReconciled is the audit action a CONFIRMED reconcile
// writes (ADR-0077 D3) — a new, distinctly-named marker, deliberately not the
// pre-ADR-0056 resolved marker saleFiscalSigningGapKind treats as
// historical-only. It is intentionally NOT in saleFiscalSigningGapKind's
// suppression check, and must never be added there (D4): a reconciled
// sale's receipt keeps its outage notice. Its only readers are the reconcile
// sweep's own eligibility query (a reconciled sale is never re-asked) and
// whatever audit/export tooling wants to see a gap was independently
// confirmed genuine.
const fiscalSignGapActionReconciled = "fiscal_signing_reconciled"

const (
	// fiscalSignReconcileStatusNotFound / -Confirmed are the ONLY two answer
	// states (contract fiscal-sign-ask.md, fiscal.sign.reconcile.ask
	// section). There is no third — the wire shape has no field that could
	// mean "sign it now", by construction.
	fiscalSignReconcileStatusNotFound  = "not-found"
	fiscalSignReconcileStatusConfirmed = "confirmed"

	// fiscalSignReconcileTierExact / -Window name the two check tiers
	// (ADR-0077 D3), persisted on the evidence row and in the marker payload
	// so an auditor can see which one confirmed a given sale:
	//   exact  — core held a fiscal.sign.start tx_id for the sale and the
	//            answer's tx_id equals it; the strong tier.
	//   window — core held NO tx_id (the D1 round trip never completed —
	//            the honestly-named degraded case) and the evidence's own
	//            start_time/log_time fall within fiscalSignReconcileWindow of
	//            the sale's failed_at. WEAKER, not absent: the tax-advisor
	//            go-live review (D5) must see this tier named as degraded and
	//            decide whether it stays enabled.
	fiscalSignReconcileTierExact  = "exact"
	fiscalSignReconcileTierWindow = "window"

	// fiscalSignReconcileActor attributes the marker to the seeded "system"
	// user (migration 001_init.sql), the same actor the unattended EOD
	// scheduler tick uses — no human operator is present when a background
	// sweep writes.
	fiscalSignReconcileActor = "system"
)

// Scheduling knobs. Vars, not consts, purely as test seams (same pattern as
// fiscalSignAskBudget / fiscalSignStartAsyncTimeout) — nothing outside
// _test.go ever reassigns them.
var (
	// fiscalSignReconcileInitialDelay / -Interval: a plain, boring periodic
	// sweep (ADR-0077 D3 — "never a retry loop, never named like one"). No
	// per-sale queue, backoff or tried-and-failed state of any kind: a tick
	// that finds nothing eligible does nothing; a sale not reconciled this
	// tick is naturally re-considered next tick because nothing marks it.
	// Idempotent by construction — confirmed once, never re-written.
	fiscalSignReconcileInitialDelay = 60 * time.Second
	fiscalSignReconcileInterval     = 5 * time.Minute
	// fiscalSignReconcileLookback bounds how far back a tick looks for
	// eligible gaps. 48h covers a multi-day-weekend outage with a full day
	// of margin; anything older stays permanently, silently unsigned on its
	// existing markers — the designed outcome, not a failure.
	fiscalSignReconcileLookback = 48 * time.Hour
	// fiscalSignReconcileWindow is the degraded tier's tolerance: how far
	// the evidence's start_time/log_time may sit from the sale's failed_at.
	// A tender's own start→finish span is seconds to a minute or two, and
	// the TSE's clock is the vendor's, not the till's, so a few minutes of
	// skew must be absorbed — but a window wide enough to admit a
	// NEIGHBOURING sale's transaction would make the tier meaningless, so
	// it stays in single-digit minutes.
	fiscalSignReconcileWindow = 10 * time.Minute
	// fiscalSignReconcileAskBudget is the per-sale ceiling around one Ask —
	// generous (this runs off the request path; nothing waits on it) but
	// bounded so a wedged signer can't hold a tick open indefinitely. Sized
	// to WasmRuntime's own netTimeout, the widest deadline a net-permitted
	// guest's handler gets anyway.
	fiscalSignReconcileAskBudget = 10 * time.Second
	// fiscalSignReconcileBatchLimit caps one tick's candidate set so one pass
	// stays bounded however long an outage lasted; the rest is picked up by
	// the following ticks, oldest first.
	fiscalSignReconcileBatchLimit = 50
)

// fiscalSignReconcileCandidatesFn is data.POSRepo.ListFiscalSignReconcileCandidates,
// indirected through a var purely so a test can assert it is NEVER reached
// on a till with no fiscal.sign.reconcile.ask subscriber (the zero-plugin
// cost guarantee) — same seam shape as pluginApplyUpdateFn.
var fiscalSignReconcileCandidatesFn = func(ctx context.Context, repo *data.POSRepo, since time.Time, limit int) ([]data.FiscalSignReconcileCandidate, error) {
	return repo.ListFiscalSignReconcileCandidates(ctx, since, limit)
}

// fiscalSignReconcileAskPayload is the reconcile request — the sale id
// always, plus whatever identifier core actually holds (ADR-0077 D3): the
// fiscal.sign.start tx_id/tx_revision when the D1 round trip captured one
// for this sale, omitted otherwise. Field names deliberately reuse
// fiscal.sign.ask's started_tx_id/started_tx_revision (contract 1.7.0) —
// it is the very same captured value, from the same source, so a signer
// implementing both points sees one vocabulary.
type fiscalSignReconcileAskPayload struct {
	SaleID            string `json:"sale_id"`
	StartedTxID       string `json:"started_tx_id,omitempty"`
	StartedTxRevision int64  `json:"started_tx_revision,omitempty"`
}

// fiscalSignReconcileAskResponse is the JSON a plugin writes to stdout to
// answer. Two states only (see the status constants). On "confirmed" it
// carries the SAME fiscalTSEEvidence shape fiscal.sign.ask's "approved"
// answer carries (contract v1.1.0) — no new evidence shape — plus the
// identifier of the transaction the signer retrieved: tx_id (the signer's
// own transaction identifier, the fiscal.sign.start vocabulary) is what the
// exact tier compares against the tx_id core supplied. tx_revision is
// informational only — recorded nowhere, checked against nothing.
type fiscalSignReconcileAskResponse struct {
	Status     string             `json:"status"`
	TxID       string             `json:"tx_id"`
	TxRevision int64              `json:"tx_revision"`
	TSE        *fiscalTSEEvidence `json:"tse,omitempty"`
}

// StartFiscalSignReconcileSweep runs the background fiscal.sign.reconcile.ask
// sweep (ADR-0077 D3, ut-docs#1520). Shape mirrors StartPluginUpdateScheduler
// exactly: a goroutine, a short initial delay, then a ticker, wg.Done() on
// ctx.Done(). Offline-first: a tick never blocks checkout (it shares nothing
// with the tender path but the DB), and a till with no reconcile subscriber
// pays one in-memory subscriber lookup per tick and nothing else — no DB
// access, no dispatch.
func StartFiscalSignReconcileSweep(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-time.After(fiscalSignReconcileInitialDelay):
		case <-ctx.Done():
			return
		}
		fiscalSignReconcileTick(ctx, d)
		t := time.NewTicker(fiscalSignReconcileInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				fiscalSignReconcileTick(ctx, d)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// fiscalSignReconcileTick runs one sweep pass: every eligible sale (see
// data.POSRepo.ListFiscalSignReconcileCandidates) is asked once, oldest
// first, and each confirmed-and-checked answer is recorded. No state is
// carried between ticks.
func fiscalSignReconcileTick(ctx context.Context, d *common.Deps) {
	// A recover() at the top, for the same reason pluginUpdateCheckTick has
	// one: an unrecovered panic in a background goroutine takes down the
	// WHOLE till process — mid-sale, on a merchant's counter — not just this
	// loop. A plugin handler runs synchronously inside EventBus.Ask on this
	// goroutine, so a guest-side panic lands here. Log it; the next tick
	// re-considers whatever was left.
	defer func() {
		if r := recover(); r != nil {
			logging.L().Errorf("[FiscalSignReconcile] recovered from panic (next tick re-considers): %v", r)
		}
	}()
	bus := plugins.SharedBus(d.Db)
	// Before ANY other work, so a till with no reconcile subscriber — the
	// entire installed base until a signer implements the point
	// (ut-docs#1521) — pays exactly one map lookup under RLock per tick.
	if !bus.HasSubscribers(fiscalSignReconcileAskEvent) {
		return
	}
	repo := data.NewPOSRepo(d.Db)
	since := time.Now().UTC().Add(-fiscalSignReconcileLookback)
	candidates, err := fiscalSignReconcileCandidatesFn(ctx, repo, since, fiscalSignReconcileBatchLimit)
	if err != nil {
		logging.L().Warnf("[FiscalSignReconcile] list candidates: %v", err)
		return
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		if err := reconcileFiscalSignGap(ctx, bus, repo, c); err != nil {
			// A dispatch-level failure (transport, handler error, a DB read
			// the check itself needs) says nothing about THIS sale and is
			// overwhelmingly likely to repeat for the next one in the same
			// pass — end the pass here rather than burn the budget N times.
			// Not a Warn: nothing is operator-actionable, and the sale's
			// own tender-time declaration already raised its alert. The
			// next tick simply re-considers it.
			logging.L().Infof("[FiscalSignReconcile] sale %s: %v — leaving the rest of this pass to the next tick", c.SaleID, err)
			return
		}
	}
}

// reconcileFiscalSignGap asks the exclusive signer about ONE eligible sale
// and records the outcome only when the answer is "confirmed", carries a
// signature, and passes whichever check tier applies. Every other outcome —
// not-found, no answer, an unusable answer, a failed check — writes NOTHING:
// the sale stays permanently, silently unsigned on its existing markers,
// which is the designed outcome, not a failure to log loudly. A non-nil
// error is returned only for a dispatch-level failure the caller should end
// the pass on; a per-sale "write nothing" outcome is nil.
func reconcileFiscalSignGap(ctx context.Context, bus *plugins.EventBus, repo *data.POSRepo, c data.FiscalSignReconcileCandidate) error {
	payload := fiscalSignReconcileAskPayload{SaleID: c.SaleID}
	heldTxID := ""
	// What core actually holds for this sale (ADR-0077 D1's best-effort
	// capture). A read failure is NOT degraded to the window tier: "couldn't
	// read the identifier" must never silently select the weaker check.
	start, ok, err := repo.GetFiscalSignStart(ctx, c.SaleID)
	if err != nil {
		return fmt.Errorf("read fiscal.sign.start capture: %w", err)
	}
	if ok {
		heldTxID = start.TxID
		payload.StartedTxID = start.TxID
		payload.StartedTxRevision = start.TxRevision
	}
	// EventBus.Ask: blocking, value-returning, first non-empty answer wins —
	// and because the fiscal signing group is `exclusive` (enforced at both
	// activation surfaces, plugins.FiscalSignExclusiveEvents), the only
	// subscriber that can exist is the manifest-verified, Ed25519-signed
	// signer for this till. That is the baseline tier (D3): nothing below
	// needs to re-derive who answered. Ask also applies the standard
	// events:receive permission check and audits a denial.
	askCtx, cancel := context.WithTimeout(ctx, fiscalSignReconcileAskBudget)
	defer cancel()
	resp, answered, err := bus.Ask(askCtx, fiscalSignReconcileAskEvent, payload)
	if err != nil {
		return fmt.Errorf("reconcile dispatch failed: %w", err)
	}
	if !answered {
		return nil
	}
	var parsed fiscalSignReconcileAskResponse
	if json.Unmarshal(resp, &parsed) != nil {
		// Answered, unusably. Unlike fiscal.sign.ask there is nothing to
		// declare here — the sale's gap is already declared — so this is
		// simply "no confirmation".
		return nil
	}
	switch parsed.Status {
	case fiscalSignReconcileStatusConfirmed:
		// The one state that can lead to a write — checked further below.
	case fiscalSignReconcileStatusNotFound:
		// The signer's own "no such transaction" — nothing changes; the
		// sale stays permanently unsigned.
		return nil
	default:
		// Anything else — including an "approved" copied from the finish
		// contract, which this point deliberately does not recognise.
		return nil
	}
	// Baseline (D3): confirmed means nothing without the signature itself
	// — the same presence test fiscal.sign.ask's evidence uses.
	if !parsed.TSE.hasSignature() {
		return nil
	}
	tier, ok := fiscalSignReconcileCheck(heldTxID, c.FailedAt, parsed)
	if !ok {
		return nil
	}
	// Evidence first, marker second: a marker without evidence would claim
	// a confirmation the system of record can't show. If the evidence write
	// fails the sale stays eligible (no marker) and the next tick retries
	// the whole idempotent sequence; if only the marker write fails, the
	// evidence row's ON CONFLICT DO NOTHING makes the next pass a no-op for
	// it and the marker gets written then.
	if err := repo.RecordFiscalTSEReconciledSignature(ctx, data.FiscalTSEReconciledSignature{
		SaleID:             c.SaleID,
		TxID:               heldTxID,
		CheckTier:          tier,
		TransactionNumber:  parsed.TSE.TransactionNumber,
		SignatureCounter:   parsed.TSE.SignatureCounter,
		SerialNumber:       parsed.TSE.SerialNumber,
		StartTime:          parsed.TSE.StartTime,
		LogTime:            parsed.TSE.LogTime,
		Signature:          parsed.TSE.Signature,
		SignatureAlgorithm: parsed.TSE.SignatureAlgorithm,
	}); err != nil {
		logging.L().Errorf("[FiscalSignReconcile] persist reconciled evidence for sale %s: %v", c.SaleID, err)
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.InsertAudit(ctx, nil, fiscalSignReconcileActor, "sale", c.SaleID, fiscalSignGapActionReconciled, map[string]any{
		"check_tier":    tier,
		"tx_id":         heldTxID,
		"failed_at":     c.FailedAt,
		"reconciled_at": now,
	}, now, ""); err != nil {
		logging.L().Errorf("[FiscalSignReconcile] %s audit marker for sale %s: %v", fiscalSignGapActionReconciled, c.SaleID, err)
		return nil
	}
	// Info, not Warn: good news is not a Problem. The receipt is unchanged
	// (D4) — this closes the compliance record's gap, nothing customer-facing.
	logging.L().Infof("[FiscalSignReconcile] sale %s: signer confirmed an existing TSE signature (check tier %s) — recorded for the audit trail; the receipt's tender-time notice is unchanged (ADR-0077 D3/D4)", c.SaleID, tier)
	return nil
}

// fiscalSignReconcileCheck applies ADR-0077 D3's two-tier check to a
// confirmed answer and reports which tier confirmed it. Pure — no I/O — so
// every branch is pinned by a table test.
//
//   - heldTxID != "": the EXACT tier, and ONLY that tier. The answer's tx_id
//     must equal the identifier core supplied in the request. A mismatch is
//     never rescued by the time window: when core holds an identifier, the
//     identifier decides.
//   - heldTxID == "": the WINDOW tier — the degraded case, explicitly weaker
//     (D3/D5). failedAt (the sale's declare-time timestamp) anchors it; every
//     evidence timestamp that is present (start_time, log_time) must parse
//     as RFC3339 and lie within ±fiscalSignReconcileWindow of the anchor, and
//     at least one must be present — evidence with no usable timestamp at
//     all has nothing to structurally check and is refused. An unparseable
//     anchor refuses too (fail closed).
func fiscalSignReconcileCheck(heldTxID, failedAt string, resp fiscalSignReconcileAskResponse) (string, bool) {
	if resp.TSE == nil {
		return "", false
	}
	if heldTxID != "" {
		if resp.TxID != heldTxID {
			return "", false
		}
		return fiscalSignReconcileTierExact, true
	}
	anchor, err := time.Parse(time.RFC3339, failedAt)
	if err != nil {
		return "", false
	}
	checked := false
	for _, raw := range []string{resp.TSE.StartTime, resp.TSE.LogTime} {
		if raw == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return "", false
		}
		if delta := ts.Sub(anchor); delta < -fiscalSignReconcileWindow || delta > fiscalSignReconcileWindow {
			return "", false
		}
		checked = true
	}
	if !checked {
		return "", false
	}
	return fiscalSignReconcileTierWindow, true
}
