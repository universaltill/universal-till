package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// fiscalSignAskEvent is the tender-phase fiscal signing extension point
// (ADR-0044 Decision 1, an instance of ADR-0041; ut-docs#675). It fires ONCE
// per sale, after payment.<key>.authorize has resolved (so the payable
// total — including any reader-reported tip — is final) and before
// CompleteSale persists the sale. The ".ask" suffix is what makes
// wasm_runtime.go dispatch it as a blocking, value-returning hook
// (EventBus.Ask), same as tax.rate.ask. Contract:
// ut-docs/reference/contracts/fiscal-sign-ask.md. The canonical constant
// lives in internal/plugins so manifest persistence can enforce the point's
// exclusivity (ADR-0041 Decision B) without importing this package.
const fiscalSignAskEvent = plugins.FiscalSignAskEvent

// fiscalSignAskBudget is the point's own tender-phase budget — ADR-0041
// Decision D's 3000ms figure, sized for a legally-required cloud TSE round
// trip. It is timed independently of the payment.<key>.authorize loop's own
// per-plugin 2s/10s deadline (ADR-0044 Decision 1): the budget is applied as
// a caller-side context deadline around the whole Ask, so a net:-holding
// plugin's wider inner deadline can never stretch this phase past 3s.
// A var, not a const, purely as a test seam (same pattern as print_api.go's
// printAsyncTimeout) — nothing outside _test.go ever reassigns it.
var fiscalSignAskBudget = 3000 * time.Millisecond

// fiscalSignAskPayload is the event payload a subscribing signing plugin
// receives — the hook's ENTIRE input contract (JSON snake_case; money as
// integer minor units at this DTO boundary, per money.Money's wire form).
// Kept to exactly what a signer needs: the amount, the per-rate VAT
// breakdown, the payment method breakdown, and the tender timestamp.
type fiscalSignAskPayload struct {
	// SaleID is the id the sale WILL be persisted under (minted before
	// CompleteSale runs, which honors a pre-set SaleInput.SaleID) — so the
	// signer's own records and core's unsigned_fiscal_signing /
	// fiscal_signing_resolved audit markers correlate on the same id.
	SaleID     string                 `json:"sale_id"`
	Currency   string                 `json:"currency"`
	Total      int64                  `json:"total"`
	TenderedAt string                 `json:"tendered_at"`
	Payments   []fiscalSignAskPayment `json:"payments"`
	// VATBreakdown aggregates the sale's lines per tax rate (net after
	// line discounts, before any sale-level discount — see the contract).
	VATBreakdown []fiscalSignAskVATLine `json:"vat_breakdown"`
	// Retry marks a background re-attempt for an already-completed sale
	// (the proceed-and-declare retry loop), so a signer can distinguish it
	// from a live tender.
	Retry bool `json:"retry,omitempty"`
}

type fiscalSignAskPayment struct {
	Method string `json:"method"`
	Amount int64  `json:"amount"`
	Tip    int64  `json:"tip_amount"`
}

type fiscalSignAskVATLine struct {
	RateBP int   `json:"rate_bp"`
	Net    int64 `json:"net"`
	Tax    int64 `json:"tax"`
}

// fiscalSignAskResponse is the JSON a plugin writes to stdout to answer.
// The three declared states (contract §responses):
//   - "approved":          the sale is signed; proceed normally.
//   - "not-this-terminal": the plugin explicitly says this sale/till isn't
//     its responsibility — ADR-0041 Decision F "decline gracefully"
//     semantics, treated exactly like no answer, NOT a failure.
//   - "unreachable":       the plugin declares its signing backend
//     unreachable — a genuine failure → proceed-and-declare.
// Since contract v1.1.0 (ut-docs#585) an "approved" answer MAY additionally
// carry a `tse` object with the §6 KassenSichV receipt evidence — see
// fiscalTSEEvidence. Purely additive: a bare {"status":"approved"} stays a
// fully valid answer (no evidence persisted/rendered for that sale), and the
// other two states gain no fields.
type fiscalSignAskResponse struct {
	Status string             `json:"status"`
	TSE    *fiscalTSEEvidence `json:"tse,omitempty"`
}

// fiscalTSEEvidence is the optional §6 KassenSichV signing evidence a signer
// may return alongside "approved" (contract fiscal-sign-ask.md v1.1.0,
// ut-docs#585): the TSE's transaction number, signature counter, serial
// number, transaction start/log time, the signature itself (base64) and the
// signing algorithm identifier. Every field is individually optional; the
// evidence as a whole counts as PRESENT only when Signature is non-empty
// (see hasSignature) — a signature-less evidence object proves nothing worth
// persisting. Field set modeled on the legal requirement, not on any
// vendor's API shape: no fiskaly sandbox/real TSE was available to copy
// field names from (the ut-docs#757 honesty constraint), so end-to-end
// verification against a real TSE is an explicit follow-up.
type fiscalTSEEvidence struct {
	TransactionNumber  int64  `json:"transaction_number,omitempty"`
	SignatureCounter   int64  `json:"signature_counter,omitempty"`
	SerialNumber       string `json:"serial_number,omitempty"`
	StartTime          string `json:"start_time,omitempty"`
	LogTime            string `json:"log_time,omitempty"`
	Signature          string `json:"signature,omitempty"`
	SignatureAlgorithm string `json:"signature_algorithm,omitempty"`
}

// hasSignature is the presence test the contract fixes: evidence without the
// signature itself (the receipt's Prüfwert) is treated exactly like no
// evidence — nothing persisted, nothing rendered, never placeholders.
func (e *fiscalTSEEvidence) hasSignature() bool {
	return e != nil && e.Signature != ""
}

const (
	fiscalSignStatusApproved        = "approved"
	fiscalSignStatusNotThisTerminal = "not-this-terminal"
	fiscalSignStatusUnreachable     = "unreachable"
)

// fiscalSignOutcome classifies one dispatch's result for the tender path.
type fiscalSignOutcome int

const (
	// fiscalSignNoSigner: no plugin subscribes to fiscal.sign.ask — the
	// zero-plugin fast path; nothing happens, nothing is recorded.
	fiscalSignNoSigner fiscalSignOutcome = iota
	// fiscalSignApproved: the signer answered "approved".
	fiscalSignApproved
	// fiscalSignNoOpinion: a clean decline ("not-this-terminal", or nobody
	// answered) — treated like no signer; NOT a failure (ADR-0041 F).
	fiscalSignNoOpinion
	// fiscalSignSkippedOffline: the till already knows it's offline, so the
	// dispatch was skipped entirely (ADR-0044 D1's known-offline
	// short-circuit) → proceed-and-declare.
	fiscalSignSkippedOffline
	// fiscalSignFailedBackend: a backend-level failure — timeout,
	// transport/handler error, or the plugin explicitly declaring its
	// backend "unreachable" — evidence the signing backend itself cannot
	// be reached right now → proceed-and-declare. The background retry
	// tick aborts early on this outcome: everything left in the queue
	// shares the same down backend, so re-paying the 3s budget per entry
	// this tick is pointless.
	fiscalSignFailedBackend
	// fiscalSignFailedEntry: a protocol-level failure — the plugin
	// answered in-budget, but with something core can't accept for THIS
	// sale (unparseable JSON, an unknown status). Signing is unproven, so
	// it's still proceed-and-declare, but it is a PER-ENTRY outcome, not
	// evidence the backend is down: the retry tick keeps going through the
	// rest of the queue (review of ut-docs#675, B3 — one
	// permanently-confusing entry must not starve every entry behind it).
	fiscalSignFailedEntry
)

// isFailure reports whether the outcome is a signing failure of either
// level — the tender path treats both identically (proceed-and-declare);
// only the retry tick's abort-early decision distinguishes them.
func (o fiscalSignOutcome) isFailure() bool {
	return o == fiscalSignFailedBackend || o == fiscalSignFailedEntry
}

// fiscalSignResult is one dispatch's outcome plus what the declare path
// needs: a human-readable reason for the journal/log, and the payload for
// the background retry queue.
type fiscalSignResult struct {
	Outcome fiscalSignOutcome
	Reason  string
	Payload fiscalSignAskPayload
	// Evidence is the §6 KassenSichV TSE evidence parsed from an approved
	// answer (contract v1.1.0, ut-docs#585) — non-nil ONLY on
	// fiscalSignApproved AND only when the answer carried a usable evidence
	// object (hasSignature); nil means "approved, nothing to persist/render",
	// exactly the pre-1.1.0 behaviour.
	Evidence *fiscalTSEEvidence
}

// dispatchFiscalSignAsk runs the fiscal.sign.ask point for one tender.
// Called from completeTender between the authorize loop and CompleteSale —
// that ordering is load-bearing (ADR-0044 Decision 1): authorize can still
// adjust the payable total (reader-reported tip) or refuse the sale outright,
// and a signature must only ever cover the final total of a sale that will
// actually be attempted.
//
// The HasSubscribers check comes before ANY other work so a zero-plugin till
// pays exactly one map lookup under RLock — no allocation, no goroutine, no
// DB access (ADR-0041 Decision A; asserted by
// TestFiscalSignAsk_ZeroPluginTillAllocatesNothing).
//
// in is mutated: when a signer is installed, in.SaleID is minted here (if
// empty) so the ask payload and the eventually-persisted sale share an id —
// CompleteSale honors a pre-set SaleID.
func dispatchFiscalSignAsk(ctx context.Context, d *common.Deps, in *pos.SaleInput) fiscalSignResult {
	bus := plugins.SharedBus(d.Db)
	if !bus.HasSubscribers(fiscalSignAskEvent) {
		return fiscalSignResult{Outcome: fiscalSignNoSigner}
	}
	if in.SaleID == "" {
		in.SaleID = uuid.NewString()
	}
	payload := buildFiscalSignPayload(in, time.Now().UTC())
	// Known-offline short-circuit (ADR-0044 Decision 1): the tender request
	// itself carries the till's declared offline state (the #offline-flag /
	// navigator.onLine signal, threaded through SaleInput.Offline — the same
	// existing signal that drives the sale row's offline/sync-queued flags).
	// Spending the 3s budget on a cloud call the till already knows cannot
	// succeed would stall checkout on every single sale while offline — a
	// real ADR-0003 regression even though it never exceeds the budget.
	if in.Offline {
		return fiscalSignResult{
			Outcome: fiscalSignSkippedOffline,
			Reason:  "known-offline: dispatch skipped without contacting the signing plugin",
			Payload: payload,
		}
	}
	return askFiscalSign(ctx, bus, payload)
}

// askFiscalSign performs one budget-bound Ask and classifies the answer.
// Shared by the live tender dispatch and the background retry tick.
func askFiscalSign(ctx context.Context, bus *plugins.EventBus, payload fiscalSignAskPayload) fiscalSignResult {
	askCtx, cancel := context.WithTimeout(ctx, fiscalSignAskBudget)
	defer cancel()
	resp, ok, err := bus.Ask(askCtx, fiscalSignAskEvent, payload)
	if err != nil {
		if errors.Is(askCtx.Err(), context.DeadlineExceeded) {
			// The budget itself expired — nothing answered in time, which
			// says nothing about whether THIS plugin's handler is broken
			// vs. the backend it talks to being slow/down. Treated as
			// backend-level so the retry tick doesn't keep re-paying the
			// full budget against every other queued sale in the same
			// tick while the timeout condition is still live.
			return fiscalSignResult{Outcome: fiscalSignFailedBackend, Reason: fmt.Sprintf("signing dispatch failed: %v", err), Payload: payload}
		}
		// A real handler/guest error (e.g. a wasm trap on this specific
		// payload) within budget: this plugin answered, badly, for THIS
		// entry — it says nothing about the other queued sales, so it must
		// not abort the retry tick early (that would silently starve every
		// entry behind a deterministically-misbehaving one, forever). Same
		// entry-level treatment as an unparseable/unknown response below.
		return fiscalSignResult{Outcome: fiscalSignFailedEntry, Reason: fmt.Sprintf("signing dispatch failed: %v", err), Payload: payload}
	}
	if !ok {
		// Nobody answered (every subscriber cleanly declined with an empty
		// response) — same graceful no-opinion EventBus.Ask already gives.
		return fiscalSignResult{Outcome: fiscalSignNoOpinion, Payload: payload}
	}
	var parsed fiscalSignAskResponse
	if json.Unmarshal(resp, &parsed) != nil {
		// Answered, but with JSON core can't read: signing is NOT proven to
		// have happened, and for a compliance-bearing point "unproven" must
		// be declared, never assumed fine. Protocol-level: the backend IS
		// answering — this entry's answer is what's broken.
		return fiscalSignResult{Outcome: fiscalSignFailedEntry, Reason: "signing plugin answered with unparseable JSON", Payload: payload}
	}
	switch parsed.Status {
	case fiscalSignStatusApproved:
		// v1.1.0 evidence rides along only when usable (hasSignature); a
		// bare or signature-less approval is the same clean approval it
		// always was — the evidence never changes the outcome.
		res := fiscalSignResult{Outcome: fiscalSignApproved, Payload: payload}
		if parsed.TSE.hasSignature() {
			res.Evidence = parsed.TSE
		}
		return res
	case fiscalSignStatusNotThisTerminal:
		// An explicit "not me" — ADR-0041 Decision F: same as no answer.
		return fiscalSignResult{Outcome: fiscalSignNoOpinion, Payload: payload}
	case fiscalSignStatusUnreachable:
		// The plugin's own authoritative "my backend is down" — treated as
		// backend-level, same as a transport failure.
		return fiscalSignResult{Outcome: fiscalSignFailedBackend, Reason: "signing backend declared unreachable by the plugin", Payload: payload}
	default:
		// Protocol-level, same as unparseable JSON: an unrecognized status
		// (e.g. a future contract version's new state) proves the backend
		// is up and talking — it says nothing about the other queued sales.
		return fiscalSignResult{Outcome: fiscalSignFailedEntry, Reason: fmt.Sprintf("signing plugin answered with unknown status %q", parsed.Status), Payload: payload}
	}
}

// buildFiscalSignPayload derives the ask payload from the finalized sale
// input, mirroring the tender handler's own totals math (pos.AmountForQuantity
// / pos.ComputeTaxBasisPoints over the same lines) so what the signer signs
// is what CompleteSale records.
func buildFiscalSignPayload(in *pos.SaleInput, now time.Time) fiscalSignAskPayload {
	subtotal, taxTotal := money.Zero, money.Zero
	perRate := map[int]*fiscalSignAskVATLine{}
	for _, l := range in.Lines {
		lineNet := pos.AmountForQuantity(l.UnitPrice, l.Qty).Sub(l.LineDiscount)
		lineTax, _ := pos.ComputeTaxBasisPoints(lineNet, l.TaxRateBasisPoints, in.TaxInclusive)
		subtotal = subtotal.Add(lineNet)
		taxTotal = taxTotal.Add(lineTax)
		agg, ok := perRate[l.TaxRateBasisPoints]
		if !ok {
			agg = &fiscalSignAskVATLine{RateBP: l.TaxRateBasisPoints}
			perRate[l.TaxRateBasisPoints] = agg
		}
		agg.Net += lineNet.Minor()
		agg.Tax += lineTax.Minor()
	}
	total := subtotal.Sub(in.SaleDiscount).Add(in.ServiceCharge)
	if !in.TaxInclusive {
		total = total.Add(taxTotal)
	}
	if total.IsNegative() {
		total = money.Zero
	}
	vat := make([]fiscalSignAskVATLine, 0, len(perRate))
	for _, v := range perRate {
		vat = append(vat, *v)
	}
	sort.Slice(vat, func(i, j int) bool { return vat[i].RateBP < vat[j].RateBP })
	payments := make([]fiscalSignAskPayment, 0, len(in.Payments))
	for _, p := range in.Payments {
		payments = append(payments, fiscalSignAskPayment{
			Method: p.MethodID,
			// NET of change handed back (review of ut-docs#675, B4): a €20
			// cash tender against a €12 sale collected €12 — the same
			// Amount.Sub(ChangeGiven) that netPayments (CompleteSale's
			// sufficiency check) and renderReceipt already compute. The
			// gross tender would corrupt the irreversible signed record's
			// payment-type breakdown.
			Amount: p.Amount.Sub(p.ChangeGiven).Minor(),
			Tip:    p.TipAmount.Minor(),
		})
	}
	return fiscalSignAskPayload{
		SaleID:       in.SaleID,
		Currency:     in.Currency,
		Total:        total.Minor(),
		TenderedAt:   now.Format(time.RFC3339),
		Payments:     payments,
		VATBreakdown: vat,
	}
}

// recordFiscalTSEEvidence persists an approved answer's §6 KassenSichV
// evidence for a sale (ut-docs#585) — best-effort and log-only on failure,
// exactly like declareUnsignedFiscalSale's bookkeeping: the sale is already
// committed and must never be unwound over an evidence write failing.
// Idempotent at the repository level (first write wins), and a nil evidence
// (bare approval, pre-1.1.0 signer) is a no-op.
func recordFiscalTSEEvidence(ctx context.Context, repo *data.POSRepo, saleID string, ev *fiscalTSEEvidence) {
	if ev == nil {
		return
	}
	if err := repo.RecordFiscalTSESignature(ctx, data.FiscalTSESignature{
		SaleID:             saleID,
		TransactionNumber:  ev.TransactionNumber,
		SignatureCounter:   ev.SignatureCounter,
		SerialNumber:       ev.SerialNumber,
		StartTime:          ev.StartTime,
		LogTime:            ev.LogTime,
		Signature:          ev.Signature,
		SignatureAlgorithm: ev.SignatureAlgorithm,
	}); err != nil {
		logging.L().Errorf("fiscal signing: persist TSE evidence for sale %s: %v", saleID, err)
	}
}

// declareUnsignedFiscalSale is the proceed-and-declare surface (ADR-0041
// Decision E / ADR-0044 Decision 1) for a sale that completed without a
// fiscal signature — every step is best-effort and log-only on failure: the
// sale is already committed and must never be unwound by its own declaration
// bookkeeping.
//
//	(a) journal flag  — sale/unsigned_fiscal_signing audit marker (mirrors
//	    the ADR-0048 unsigned_override marker block exactly);
//	(b) receipt notice — derived from that marker by both render paths
//	    (renderReceipt via HasAuditEntry, print_api.go's ESC/POS doc);
//	(c) operator alert — a Warn into the Problems ring (logging.Recent);
//	(d) background retry — queued under common.KeyPendingFiscalSignRetries,
//	    drained by fiscalSignRetryTick.
//
// Deliberately NOT here: fiscal.KeyTSEFailingSince. ADR-0048 Decision 1
// reserves that key for "the TSE itself is known bad" (expired cert, dongle
// pulled, provider-reported fault) — a strictly narrower condition than "we
// currently can't reach it" — and EVERY failure this card can observe is a
// reachability outcome: the contract's three response states
// (approved / not-this-terminal / unreachable) plus timeout, transport
// error and an unusable answer give a plugin no way to say "my TSE is
// confirmed broken". Stamping the key from here would hard-block the shop's
// NEXT sale via the ADR-0048 gate over a mere outage — the exact
// offline-first regression that ADR forbids (review of ut-docs#675, B1). A
// future contract version adding a TSE-confirmed-broken response state
// would be the right trigger for that key; this card never drives it — and
// correspondingly never CLEARS it either (see fiscalSignRetryTick).
func declareUnsignedFiscalSale(ctx context.Context, d *common.Deps, repo *data.POSRepo, saleID, actorID string, res fiscalSignResult) {
	now := time.Now().UTC().Format(time.RFC3339)
	knownOffline := res.Outcome == fiscalSignSkippedOffline

	// (a) Journal marker. Same InsertAudit shape as the unsigned_override
	// block in completeTender: best-effort after the fact, logged never
	// fatal.
	if auditErr := repo.InsertAudit(ctx, nil, actorID, "sale", saleID, "unsigned_fiscal_signing", map[string]any{
		"reason":        res.Reason,
		"known_offline": knownOffline,
		"failed_at":     now,
	}, now, ""); auditErr != nil {
		log.Printf("fiscal signing: unsigned_fiscal_signing audit marker for sale %s failed: %v", saleID, auditErr)
	}

	// (c) Operator alert — Warn populates the Problems ring
	// (logging.remember), same surface warnIfStockNegative uses for its
	// per-sale condition; the cloud heartbeat's problems digest picks it up.
	logging.L().Warnf("fiscal signing: sale %s completed UNSIGNED (%s) — journaled, receipt carries an outage notice, signing will be retried in the background (fiscal.sign.ask proceed-and-declare, ADR-0044)", saleID, res.Reason)

	// (d) Queue the background retry (survives restart via settings).
	if err := enqueueFiscalSignRetry(ctx, d, pendingFiscalSignRetry{
		SaleID:   saleID,
		FailedAt: now,
		Payload:  res.Payload,
	}); err != nil {
		logging.L().Errorf("fiscal signing: queue background retry for sale %s: %v", saleID, err)
	}
}

// saleHasUnresolvedSigningGap decides the receipt outage notice for both
// render paths (renderReceipt's flag in pos_api.go, the ESC/POS Meta line in
// print_api.go): true only while the sale carries an unsigned_fiscal_signing
// marker WITHOUT a later fiscal_signing_resolved row — a sale the background
// retry already signed prints clean on any reprint (review of ut-docs#675,
// alongside B1-B4). Errors degrade conservatively: can't read the marker →
// no notice (the authoritative record is the audit row itself, same policy
// as before); marker present but can't read the resolution → keep the
// notice, which was truthful at write time.
func saleHasUnresolvedSigningGap(ctx context.Context, repo *data.POSRepo, saleID string) bool {
	hasGap, err := repo.HasAuditEntry(ctx, "sale", saleID, "unsigned_fiscal_signing")
	if err != nil || !hasGap {
		return false
	}
	resolved, err := repo.HasAuditEntry(ctx, "sale", saleID, "fiscal_signing_resolved")
	if err != nil {
		return true
	}
	return !resolved
}

// --- background retry (ADR-0044 D1: "retry signing in the background") -----

// pendingFiscalSignRetry is one queued unsigned sale, persisted as JSON
// under common.KeyPendingFiscalSignRetries (same persist-then-retry shape as
// setup_base_plugins.go's pending list — survives a process restart).
type pendingFiscalSignRetry struct {
	SaleID   string               `json:"sale_id"`
	FailedAt string               `json:"failed_at"`
	Payload  fiscalSignAskPayload `json:"payload"`
}

// fiscalSignRetryInitialDelay/-Interval shape the retry loop, mirroring
// StartBasePluginRetry's "short delay, then a ticker" idiom. Like that loop
// (and unlike sync_admin.go's capped broken-plugin refetch) it retries
// INDEFINITELY: an unsigned sale has no terminal failure to give up on — it
// only needs the signing backend reachable again, and every queued sale must
// eventually be signed. The interval is tighter than the base-plugin
// installer's 5m because an open signing gap is compliance-relevant (the
// shop is trading with unsigned sales on the books), but still far looser
// than the 30s sync tick — one attempt burns up to the 3s budget, and the
// tick aborts on the first still-down answer rather than paying it per
// queued sale.
const (
	fiscalSignRetryInitialDelay = 30 * time.Second
	fiscalSignRetryInterval     = 2 * time.Minute
)

// fiscalSignRetryMu serializes list read-modify-write between the tender
// path's enqueue and the background tick.
var fiscalSignRetryMu sync.Mutex

func loadPendingFiscalSignRetries(ctx context.Context, d *common.Deps) ([]pendingFiscalSignRetry, error) {
	if d.Settings == nil {
		return nil, nil
	}
	raw, ok, err := d.Settings.Get(ctx, common.KeyPendingFiscalSignRetries)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return nil, err
	}
	var entries []pendingFiscalSignRetry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func savePendingFiscalSignRetries(ctx context.Context, d *common.Deps, entries []pendingFiscalSignRetry) error {
	if d.Settings == nil {
		return fmt.Errorf("settings store unavailable")
	}
	if len(entries) == 0 {
		return d.Settings.Set(ctx, common.KeyPendingFiscalSignRetries, "")
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return d.Settings.Set(ctx, common.KeyPendingFiscalSignRetries, string(raw))
}

func enqueueFiscalSignRetry(ctx context.Context, d *common.Deps, entry pendingFiscalSignRetry) error {
	fiscalSignRetryMu.Lock()
	defer fiscalSignRetryMu.Unlock()
	entries, err := loadPendingFiscalSignRetries(ctx, d)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.SaleID == entry.SaleID {
			return nil // already queued (idempotent)
		}
	}
	return savePendingFiscalSignRetries(ctx, d, append(entries, entry))
}

// fiscalSignRetryTick is one pass of the background retry: re-ask each
// queued unsigned sale, drop what succeeds (with a fiscal_signing_resolved
// audit row against the same sale — audit rows are append-only, so recovery
// gets its own marker rather than mutating the original), and stop early
// ONLY at the first BACKEND-level failure (transport error, budget timeout,
// declared "unreachable"): the backlog shares one backend, so once that
// backend is provably down there is no point paying the 3s budget once per
// queued sale this tick. A PROTOCOL-level failure (unparseable JSON,
// unknown status) is a per-entry outcome — the entry stays queued but the
// tick continues with the rest, so one permanently-confusing entry can
// never starve every sale behind it (review of ut-docs#675, B3).
//
// fiscal.KeyTSEFailingSince is deliberately not touched here, in either
// direction — this mechanism never sets it (see declareUnsignedFiscalSale's
// ADR-0048 Decision 1 note), so clearing it on drain would emit a false
// "TSE is fine now" against whatever other mechanism legitimately set it.
//
// The list lock is NOT held across the asks (each can burn up to the 3s
// budget, and a live tender's enqueue must never wait on that): resolved
// sale IDs are removed from a freshly re-loaded list at the end, so an entry
// enqueued mid-tick survives untouched.
func fiscalSignRetryTick(ctx context.Context, d *common.Deps) {
	pending, err := func() ([]pendingFiscalSignRetry, error) {
		fiscalSignRetryMu.Lock()
		defer fiscalSignRetryMu.Unlock()
		return loadPendingFiscalSignRetries(ctx, d)
	}()
	if err != nil {
		logging.L().Warnf("fiscal signing retry: load pending list: %v", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	bus := plugins.SharedBus(d.Db)
	if !bus.HasSubscribers(fiscalSignAskEvent) {
		// Signer uninstalled or currently broken — keep the backlog; the
		// next tick after it heals will drain it.
		return
	}
	repo := data.NewPOSRepo(d.Db)
	var resolvedIDs []string
	for _, entry := range pending {
		payload := entry.Payload
		payload.Retry = true
		res := askFiscalSign(ctx, bus, payload)
		if res.Outcome == fiscalSignApproved {
			resolvedIDs = append(resolvedIDs, entry.SaleID)
			// A background re-sign that returned evidence records it too
			// (ut-docs#585): a recovered sale's reprints show the same §6
			// field set a live-signed sale's do. Idempotent — if evidence
			// somehow already exists for the sale, the first write stands.
			recordFiscalTSEEvidence(ctx, repo, entry.SaleID, res.Evidence)
			now := time.Now().UTC().Format(time.RFC3339)
			if auditErr := repo.InsertAudit(ctx, nil, "", "sale", entry.SaleID, "fiscal_signing_resolved", map[string]any{
				"resolved_at":       now,
				"originally_failed": entry.FailedAt,
			}, now, ""); auditErr != nil {
				logging.L().Errorf("fiscal signing retry: resolved marker for sale %s failed: %v", entry.SaleID, auditErr)
			}
			logging.L().Infof("fiscal signing retry: sale %s signed on background retry (originally failed %s)", entry.SaleID, entry.FailedAt)
			continue
		}
		if res.Outcome == fiscalSignNoOpinion {
			// The active signer says this sale isn't its responsibility
			// (e.g. the shop swapped providers since the failure). Keep it
			// queued — dropping it would silently lose a declared gap — but
			// keep trying the rest: a not-me answer is cheap.
			continue
		}
		if res.Outcome == fiscalSignFailedEntry {
			// Protocol-level: THIS entry's answer is unusable (unknown
			// status, unparseable JSON) but the backend is demonstrably up
			// and answering. Keep the entry queued for a future tick (a
			// contract/provider fix may unstick it) and keep going — the
			// sales behind it deserve their attempt this tick (B3).
			logging.L().Warnf("fiscal signing retry: sale %s still unsigned (%s) — kept queued, continuing with the rest of the backlog", entry.SaleID, res.Reason)
			continue
		}
		// Backend-level failure: everything left shares the same down
		// backend — stop burning the budget this tick.
		break
	}
	if _, err := removeResolvedFiscalSignRetries(ctx, d, resolvedIDs); err != nil {
		logging.L().Errorf("fiscal signing retry: persist pending list: %v", err)
	}
}

// removeResolvedFiscalSignRetries drops the given sale IDs from the
// persisted pending list under the lock, re-loading first so entries
// enqueued while the tick was dispatching are preserved. Returns how many
// entries remain queued.
func removeResolvedFiscalSignRetries(ctx context.Context, d *common.Deps, resolvedIDs []string) (int, error) {
	fiscalSignRetryMu.Lock()
	defer fiscalSignRetryMu.Unlock()
	current, err := loadPendingFiscalSignRetries(ctx, d)
	if err != nil {
		return 0, err
	}
	if len(resolvedIDs) == 0 {
		return len(current), nil
	}
	resolved := make(map[string]bool, len(resolvedIDs))
	for _, id := range resolvedIDs {
		resolved[id] = true
	}
	remaining := make([]pendingFiscalSignRetry, 0, len(current))
	for _, e := range current {
		if !resolved[e.SaleID] {
			remaining = append(remaining, e)
		}
	}
	if len(remaining) != len(current) {
		if err := savePendingFiscalSignRetries(ctx, d, remaining); err != nil {
			return len(remaining), err
		}
	}
	return len(remaining), nil
}

// StartFiscalSignRetry launches the background half of proceed-and-declare
// (ADR-0044 Decision 1: "a background retry re-attempts signing the moment
// the backend is reachable again"). Shape mirrors StartBasePluginRetry
// exactly — a goroutine, a short initial delay, then a ticker, everything
// silent-and-retry, wg.Done() on ctx.Done(). Wired in internal/pages/init.go
// alongside StartBasePluginRetry, as part of the app's normal boot sequence.
func StartFiscalSignRetry(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-time.After(fiscalSignRetryInitialDelay):
		case <-ctx.Done():
			return
		}
		fiscalSignRetryTick(ctx, d)
		t := time.NewTicker(fiscalSignRetryInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				fiscalSignRetryTick(ctx, d)
			case <-ctx.Done():
				return
			}
		}
	}()
}
