package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
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
// CompleteSale persists the sale — and NEVER again for that sale: there is
// no background re-ask for a sale that completed unsigned (ADR-0056,
// ut-docs#839 — TSE vendors do not permit belated signing of a completed
// transaction; the unsigned declaration is the sale's permanent record).
// The ".ask" suffix is what makes wasm_runtime.go dispatch it as a
// blocking, value-returning hook (EventBus.Ask), same as tax.rate.ask.
// Contract: ut-docs/reference/contracts/fiscal-sign-ask.md. The canonical
// constant lives in internal/plugins so manifest persistence can enforce
// the point's exclusivity (ADR-0041 Decision B) without importing this
// package.
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
	SaleID string `json:"sale_id"`
	// SaleType lets the signer tell a refund/return apart from a sale
	// (ut-docs#1203, contract 1.6.0) — before it existed a €2.40 refund and
	// a €2.40 sale produced byte-for-byte identical payloads, so a DSFinV-K
	// signer had no way to record the return as a Rückgabe rather than as
	// positive turnover (an irreversible wrong TSE record). Mirrors
	// pos.SaleInput.SaleType verbatim and carries the exact two values it
	// already uses — "sale" | "return" — the same key and values the sibling
	// plugins.SaleCompletedEvent.SaleType already puts on the wire. Never
	// omitted: every sale has a type.
	SaleType   string                 `json:"sale_type"`
	Currency   string                 `json:"currency"`
	Total      int64                  `json:"total"`
	TenderedAt string                 `json:"tendered_at"`
	Payments   []fiscalSignAskPayment `json:"payments"`
	// VATBreakdown aggregates the sale's lines per tax rate (net after
	// line discounts, before any sale-level discount — see the contract).
	VATBreakdown []fiscalSignAskVATLine `json:"vat_breakdown"`
	// TaxInclusive is the till's pricing mode for this sale (ut-docs#834,
	// contract 1.2.0) — mirrors pos.SaleInput.TaxInclusive /
	// pos.ComputeTaxBasisPoints' own third argument, the exact switch
	// buildFiscalSignPayload already used internally to compute Net/Tax
	// below, just no longer hidden from the signer. Exclusive: Net is the
	// true net, gross is Net+Tax. Inclusive (the German norm): Net already
	// HOLDS the gross, tax is contained within it, gross is Net. A signer
	// building a Beleg's gross-per-rate line from `net`/`tax` alone had to
	// infer this by testing which reading reconciles with Total; this flag
	// removes the inference.
	TaxInclusive bool `json:"tax_inclusive"`
	// SaleDiscount / ServiceCharge are the sale-level (not per-line)
	// amounts already folded into Total — mirrors pos.SaleInput.SaleDiscount
	// / .ServiceCharge verbatim (minor units, money.Money wire form).
	// SaleDiscount is still NOT reflected anywhere in VATBreakdown
	// (ut-docs#834): a signer apportions it across rates itself, per the
	// contract's recommended method. ServiceCharge, since contract 1.5.0
	// (ADR-0061 Decision 5), IS apportioned into VATBreakdown by core — its
	// net and tax are folded into the existing per-rate lines via the same
	// shared pos.ApportionServiceChargeTax the tender path's
	// computeSaleTotals uses, so the two can never drift. The flat field is
	// RETAINED for display/reconciliation only; a signer must no longer
	// apportion it itself (doing both double-counts — see the contract's
	// 1.5.0 changelog). Both omitted when zero (the common case) so an
	// existing signer that never reads them sees no shape change.
	SaleDiscount  int64 `json:"sale_discount,omitempty"`
	ServiceCharge int64 `json:"service_charge,omitempty"`
	// StartedTxID / StartedTxRevision (ADR-0077 D2, ut-docs#1519) echo back
	// the identifier core captured from this same sale's fiscal.sign.start
	// dispatch, when the best-effort round trip completed in time — omitted
	// (the common "goroutine hasn't completed yet" case, or a till with no
	// fiscal.sign.start subscriber at all) exactly like SaleDiscount/
	// ServiceCharge above when zero. Genuinely additive: an existing signer
	// that has never heard of fiscal.sign.start ignores both fields exactly
	// as it ignores any other unrecognized JSON, with zero behavior change.
	StartedTxID       string `json:"started_tx_id,omitempty"`
	StartedTxRevision int64  `json:"started_tx_revision,omitempty"`
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
//
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
	// fiscalSignStatusCannotSign (contract 1.3.0, ut-docs#835): the plugin
	// declares THIS sale cannot be signed as presented — a property of the
	// sale's own data (e.g. a tip or sale-level discount the signer can't
	// reconcile, see ut-docs#833/#834), deterministic, unlike "unreachable"
	// (a backend-level condition). Routed to fiscalSignCannotSign (below)
	// so the journal/receipt wording never implies a connectivity outage
	// for a sale that was never going to sign.
	fiscalSignStatusCannotSign = "cannot-sign"
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
	// fiscalSignFailedBackend: a backend-level failure — the ask's own
	// budget genuinely expiring (context.DeadlineExceeded — nothing
	// answered in time, said nothing about the reason) or the plugin
	// explicitly declaring its backend "unreachable" — evidence the
	// signing backend itself cannot be reached right now →
	// proceed-and-declare. Deliberately NOT a bare transport/handler error
	// within budget (askFiscalSign routes that to fiscalSignFailedEntry
	// instead — see its own comment). The three failure kinds are treated
	// identically at tender time; keeping them distinct records WHY
	// signing failed, which any future reconciliation work will need
	// (ADR-0056's deferred follow-up).
	fiscalSignFailedBackend
	// fiscalSignFailedEntry: a protocol-level failure — the plugin
	// answered in-budget, but with something core can't accept for THIS
	// sale (unparseable JSON, an unknown status). Signing is unproven, so
	// it's still proceed-and-declare; unlike fiscalSignFailedBackend it
	// says the backend IS up and answering — this sale's answer is what
	// was broken.
	fiscalSignFailedEntry
	// fiscalSignCannotSign (ut-docs#835): the plugin explicitly declared
	// this SALE cannot be signed as presented — deterministic, a property
	// of the sale's own data, not of the backend's reachability. It is
	// journaled and worded differently (declareUnsignedFiscalSale,
	// saleFiscalSigningGapKind): never as a connectivity outage, since it
	// wasn't one.
	fiscalSignCannotSign
)

// isFailure reports whether the outcome is a signing failure of any kind —
// the tender path treats all three identically (proceed-and-declare,
// permanently since ADR-0056/ut-docs#839); the distinct kinds record why
// signing failed at tender time, useful for future reconciliation work.
func (o fiscalSignOutcome) isFailure() bool {
	return o == fiscalSignFailedBackend || o == fiscalSignFailedEntry || o == fiscalSignCannotSign
}

// fiscalSignResult is one dispatch's outcome plus what the declare path
// needs: a human-readable reason for the journal/log. Deliberately carries
// no copy of the request payload — ADR-0056/ut-docs#839 removed the last
// consumer that needed one (the retry queue), and a field like that is
// exactly the kind of dormant machinery a future edit could "helpfully"
// wire back up without rediscovering why re-signing was disabled; nothing
// here should make that easy.
type fiscalSignResult struct {
	Outcome fiscalSignOutcome
	Reason  string
	// Evidence is the §6 KassenSichV TSE evidence parsed from an approved
	// answer (contract v1.1.0, ut-docs#585) — non-nil ONLY on
	// fiscalSignApproved AND only when the answer carried a usable evidence
	// object (hasSignature); nil means "approved, nothing to persist/render",
	// exactly the pre-1.1.0 behaviour.
	Evidence *fiscalTSEEvidence
}

// defaultedSaleType mirrors pos.CompleteSale's own empty-SaleType fallback
// (internal/pos/sales.go): both fiscal dispatch payloads (fiscal.sign.start
// and fiscal.sign.ask) run BEFORE CompleteSale on the same *SaleInput, so a
// caller relying on CompleteSale's default alone would otherwise ship an
// empty sale_type on the signed/started record while the sale itself is
// later recorded as "sale" — the payload must never disagree with what
// CompleteSale ultimately persists.
func defaultedSaleType(saleType string) string {
	if saleType == "" {
		return "sale"
	}
	return saleType
}

// fiscalSignStartEvent is the ADR-0077 Decision 1 dispatch point — see
// plugins.FiscalSignStartEvent's own doc comment for why it is a separate
// event key from fiscal.sign.ask, not a new field/action on it.
const fiscalSignStartEvent = plugins.FiscalSignStartEvent

// fiscalSignStartAsyncTimeout bounds the background goroutine
// dispatchFiscalSignStart spawns — NOT a deadline the tender path races
// (nothing on that path waits for this goroutine at all, per ADR-0077 D1),
// purely a safety ceiling so a wedged or slow-network signer can't leak the
// goroutine forever. Sized a little above wasmRuntime's own netTimeout
// (10s, the widened deadline a net:*/tcp:*-permitted plugin's handler gets)
// since that is the slowest a well-behaved handler invoked via EventBus.Ask
// is expected to take; a CPU-bound guest is separately bounded by wazero's
// own WithCloseOnContextDone enforcement of the handler's own timeout. A
// var, not a const, purely as a test seam (same pattern as
// fiscalSignAskBudget above) — nothing outside _test.go ever reassigns it.
var fiscalSignStartAsyncTimeout = 15 * time.Second

// fiscalSignStartPayload is the event payload a subscribing signing plugin
// receives for fiscal.sign.start — deliberately minimal (ADR-0077 D1): no
// total, no VAT breakdown, no payments — none of that is final yet at this
// point in the tender, and SIGN DE's own `start` doesn't need it either per
// fiskaly's own integration guidance (quoted in ADR-0077's Context).
type fiscalSignStartPayload struct {
	SaleID    string `json:"sale_id"`
	SaleType  string `json:"sale_type"`
	StartedAt string `json:"started_at"`
}

// fiscalSignStartResponse is the JSON a plugin writes to stdout to answer
// fiscal.sign.start. Contract fiscal-sign-ask.md's fiscal.sign.start section:
// "acknowledged" is the only status this dispatch ever acts on — anything
// else (a different status, no answer at all, a transport/handler error) is
// silently ignored, matching the point's own best-effort framing: there is
// nothing to declare or fall back to here, unlike fiscal.sign.ask, since a
// missed round trip only means started_tx_id/started_tx_revision stay
// omitted on the later finish dispatch.
type fiscalSignStartResponse struct {
	Status     string `json:"status"`
	TxID       string `json:"tx_id"`
	TxRevision int64  `json:"tx_revision"`
}

// dispatchFiscalSignStart fires the ADR-0077 Decision 1 fiscal.sign.start
// point. Called from completeTender (and its refund/return mirrors)
// immediately after the ADR-0048 hard gate has allowed the sale to proceed
// and BEFORE dispatchFiscalSignAsk / the payment.<key>.authorize loop — that
// ordering is load-bearing (ADR-0077 D1): firing after the one earlier check
// capable of refusing the sale outright avoids creating a TSE-side
// transaction for a sale that was never going to happen at all.
//
// The HasSubscribers check comes before ANY other work so a till with no
// fiscal.sign.start subscriber pays exactly one map lookup under RLock — no
// allocation, no goroutine (ADR-0041 Decision A's zero-plugin-cost guarantee,
// extended by ADR-0077 D1 to cover this second dispatch point too).
//
// in is mutated: in.SaleID is minted here (if empty) so this dispatch and
// the later fiscal.sign.ask dispatch for the same tender share an id — per
// ADR-0077 D1, minting happens when EITHER dispatch has a subscriber, so
// whichever of the two runs first (this one always does, by call order) does
// the minting and the other sees a non-empty SaleID already there.
//
// Never blocks the tender path: the actual dispatch — and the best-effort
// tx_id/tx_revision capture — runs on a goroutine tracked via d.AsyncWork
// (same pattern as printReceiptAsync/kitchen_print.go), which this function
// returns without waiting for.
//
// ctx is accepted (matching dispatchFiscalSignAsk's own signature) but
// genuinely unused in this function's own body — review finding, ut-docs#1519:
// the goroutine deliberately uses context.Background() instead (it must
// outlive the request), and none of the synchronous pre-checks above it
// (HasSubscribers, the offline check) take a context. Kept for call-site
// symmetry with dispatchFiscalSignAsk and in case a future synchronous
// pre-check here needs one.
func dispatchFiscalSignStart(ctx context.Context, d *common.Deps, in *pos.SaleInput) {
	bus := plugins.SharedBus(d.Db)
	if !bus.HasSubscribers(fiscalSignStartEvent) {
		return
	}
	if in.SaleID == "" {
		in.SaleID = uuid.NewString()
	}
	// Known-offline short-circuit applies to fiscal.sign.start too (ADR-0077
	// D1 — explicitly called out as missing from the ADR's own first draft):
	// dispatch never reaches the plugin at all for a tender the till already
	// knows is offline, exactly as it skips the existing fiscal.sign.ask
	// dispatch today.
	if in.Offline {
		return
	}
	payload := fiscalSignStartPayload{
		SaleID:    in.SaleID,
		SaleType:  defaultedSaleType(in.SaleType),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	saleID := in.SaleID
	repo := data.NewPOSRepo(d.Db)
	d.AsyncWork.Add(1)
	go func() {
		defer d.AsyncWork.Done()
		// fiscalSignStartAsyncTimeout is a context deadline, not an
		// enforcement mechanism, for a Go handler — review finding,
		// ut-docs#1519: EventBus.Ask calls the subscriber's handler
		// synchronously and does not itself select on ctx.Done() around
		// that call, so a handler that ignores its context argument is NOT
		// interrupted by askCtx expiring; the goroutine's own call to Ask
		// simply keeps waiting on that handler regardless. For a wasm
		// signer this is moot for the opposite reason: WasmRuntime.HandleEvent
		// applies its own tighter deadline (timeoutFor, at most netTimeout —
		// currently 10s) via wazero's WithCloseOnContextDone, which actually
		// terminates the guest. What this timeout DOES bound: a well-behaved
		// Go handler (in-process, non-wasm — e.g. a future built-in signer)
		// that itself honors ctx.Done(), and it bounds how long THIS
		// goroutine's own stack frame is considered "in flight" for
		// debugging/observability purposes. Not a budget the plugin is
		// expected to race — nothing on the tender path waits for it either
		// way.
		//
		// EventBus.Ask, not Publish, deliberately: fiscal.sign.start is not
		// an ".ask"/".authorize"/".refund"-suffixed event, so
		// WasmRuntime.Sync never puts it in Blocking dispatch mode, and a
		// plain Publish would only enqueue it onto the plugin's own
		// asynchronous channel-drain loop with no response ever surfaced
		// back here. Ask bypasses dispatch-mode entirely and calls the
		// subscriber's handler directly and synchronously — exactly the
		// round trip this best-effort capture needs, just from this
		// goroutine rather than the request path.
		askCtx, cancel := context.WithTimeout(context.Background(), fiscalSignStartAsyncTimeout)
		defer cancel()
		resp, ok, err := bus.Ask(askCtx, fiscalSignStartEvent, payload)
		if err != nil || !ok {
			return
		}
		var parsed fiscalSignStartResponse
		if json.Unmarshal(resp, &parsed) != nil {
			return
		}
		// Validate the plugin's own answer before trusting it with a
		// compliance-bearing record (CLAUDE.md: "Validate all external
		// input (users, plugins, devices)" — review finding, ut-docs#1519):
		// only "acknowledged" with a non-empty tx_id is ever acted on (see
		// fiscalSignStartResponse's own doc comment), a length cap guards
		// against a misbehaving/hostile plugin writing an unbounded string
		// into a row later echoed verbatim into every fiscal.sign.ask
		// request for this sale, and a negative revision is nonsensical
		// (fiskaly's own revision numbering starts at 1 and only increases).
		const maxFiscalSignStartTxIDLen = 256
		if parsed.Status != "acknowledged" || parsed.TxID == "" ||
			len(parsed.TxID) > maxFiscalSignStartTxIDLen || parsed.TxRevision < 0 {
			return
		}
		recCtx, recCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer recCancel()
		if err := repo.RecordFiscalSignStart(recCtx, saleID, parsed.TxID, parsed.TxRevision); err != nil {
			// Info, not Warn (review finding, ut-docs#1519): this point's
			// own contract states it raises no operator alert on any
			// failure — a failed best-effort persist here is not
			// operator-actionable (unlike declareUnsignedFiscalSale's
			// Warnf, which backs a real receipt/journal gap). Still logged,
			// for developer-facing observability, just not into the
			// Problems ring Warnf feeds.
			logging.L().Infof("fiscal signing: persist fiscal.sign.start tx_id for sale %s: %v", saleID, err)
		}
	}()
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
	// Known-offline short-circuit (ADR-0044 Decision 1): the tender request
	// itself carries the till's declared offline state (the #offline-flag /
	// navigator.onLine signal, threaded through SaleInput.Offline — the same
	// existing signal that drives the sale row's offline/sync-queued flags).
	// Spending the 3s budget on a cloud call the till already knows cannot
	// succeed would stall checkout on every single sale while offline — a
	// real ADR-0003 regression even though it never exceeds the budget.
	// Checked BEFORE building the payload / the ADR-0077 D2 lookup just
	// below: a known-offline tender skips this dispatch entirely, so neither
	// is worth doing.
	if in.Offline {
		return fiscalSignResult{
			Outcome: fiscalSignSkippedOffline,
			Reason:  "known-offline: dispatch skipped without contacting the signing plugin",
		}
	}
	payload := buildFiscalSignPayload(in, time.Now().UTC())
	// ADR-0077 D2: echo back whatever fiscal.sign.start (Decision 1) managed
	// to capture for THIS sale — best-effort read, exactly as best-effort as
	// the write it's reading. A miss (no row, or the read itself fails) just
	// leaves the two fields at their zero value, which json:",omitempty"
	// already omits from the wire payload — identical to a till with no
	// fiscal.sign.start subscriber at all. Never blocks or slows this
	// dispatch: one indexed lookup on the sale's own primary key.
	//
	// Guarded on fiscal.sign.start's own HasSubscribers (review finding,
	// ut-docs#1519): without this, every till running ONLY a fiscal.sign.ask
	// signer (the entire installed base until a signer also implements
	// fiscal.sign.start, ut-docs#1521) pays this SELECT on every single
	// sale, forever, for a row that provably never exists.
	if bus.HasSubscribers(fiscalSignStartEvent) {
		start, ok, err := data.NewPOSRepo(d.Db).GetFiscalSignStart(ctx, in.SaleID)
		if err != nil {
			logging.L().Warnf("fiscal signing: read fiscal.sign.start capture for sale %s: %v", in.SaleID, err)
		} else if ok {
			payload.StartedTxID = start.TxID
			payload.StartedTxRevision = start.TxRevision
		}
	}
	return askFiscalSign(ctx, bus, payload)
}

// askFiscalSign performs one budget-bound Ask and classifies the answer.
// Called only from the live tender dispatch — the one and only time a sale
// is ever asked (ADR-0056, ut-docs#839).
func askFiscalSign(ctx context.Context, bus *plugins.EventBus, payload fiscalSignAskPayload) fiscalSignResult {
	askCtx, cancel := context.WithTimeout(ctx, fiscalSignAskBudget)
	defer cancel()
	resp, ok, err := bus.Ask(askCtx, fiscalSignAskEvent, payload)
	if err != nil {
		if errors.Is(askCtx.Err(), context.DeadlineExceeded) {
			// The budget itself expired — nothing answered in time, which
			// says nothing about whether THIS plugin's handler is broken
			// vs. the backend it talks to being slow/down. Classified
			// backend-level: the failure is about reachability, not this
			// sale's own data.
			return fiscalSignResult{Outcome: fiscalSignFailedBackend, Reason: fmt.Sprintf("signing dispatch failed: %v", err)}
		}
		// A real handler/guest error (e.g. a wasm trap on this specific
		// payload) within budget: this plugin answered, badly, for THIS
		// sale. Same protocol-level treatment as an unparseable/unknown
		// response below.
		return fiscalSignResult{Outcome: fiscalSignFailedEntry, Reason: fmt.Sprintf("signing dispatch failed: %v", err)}
	}
	if !ok {
		// Nobody answered (every subscriber cleanly declined with an empty
		// response) — same graceful no-opinion EventBus.Ask already gives.
		return fiscalSignResult{Outcome: fiscalSignNoOpinion}
	}
	var parsed fiscalSignAskResponse
	if json.Unmarshal(resp, &parsed) != nil {
		// Answered, but with JSON core can't read: signing is NOT proven to
		// have happened, and for a compliance-bearing point "unproven" must
		// be declared, never assumed fine. Protocol-level: the backend IS
		// answering — this entry's answer is what's broken.
		return fiscalSignResult{Outcome: fiscalSignFailedEntry, Reason: "signing plugin answered with unparseable JSON"}
	}
	switch parsed.Status {
	case fiscalSignStatusApproved:
		// v1.1.0 evidence rides along only when usable (hasSignature); a
		// bare or signature-less approval is the same clean approval it
		// always was — the evidence never changes the outcome.
		res := fiscalSignResult{Outcome: fiscalSignApproved}
		if parsed.TSE.hasSignature() {
			res.Evidence = parsed.TSE
		}
		return res
	case fiscalSignStatusNotThisTerminal:
		// An explicit "not me" — ADR-0041 Decision F: same as no answer.
		return fiscalSignResult{Outcome: fiscalSignNoOpinion}
	case fiscalSignStatusUnreachable:
		// The plugin's own authoritative "my backend is down" — treated as
		// backend-level, same as a transport failure.
		return fiscalSignResult{Outcome: fiscalSignFailedBackend, Reason: "signing backend declared unreachable by the plugin"}
	case fiscalSignStatusCannotSign:
		// The plugin's own authoritative "not THIS backend is down, THIS
		// sale can't be signed as presented" (ut-docs#835) — a property of
		// the sale's own data, not evidence the backend is unreachable.
		return fiscalSignResult{Outcome: fiscalSignCannotSign, Reason: "signing plugin declared this sale cannot be signed as presented"}
	default:
		// Protocol-level, same as unparseable JSON: an unrecognized status
		// (e.g. a future contract version's new state) proves the backend
		// is up and talking — this sale's answer is what core can't accept.
		return fiscalSignResult{Outcome: fiscalSignFailedEntry, Reason: fmt.Sprintf("signing plugin answered with unknown status %q", parsed.Status)}
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
	// ADR-0061 Decision 2 / contract 1.5.0: fold the service charge's
	// apportioned net/tax into the per-rate lines via the SAME shared
	// function computeSaleTotals taxes it with — never a local re-derivation,
	// so the signed breakdown and the persisted totals cannot drift. The
	// flat ServiceCharge field below stays display/reconciliation-only.
	var chargeTax money.Money
	for _, b := range pos.ApportionServiceChargeTax(in.ServiceCharge, pos.ChargeTaxLinesFromSale(in.Lines), in.TaxInclusive, in.ServiceChargeTaxBasisBP) {
		agg, ok := perRate[b.RateBP]
		if !ok {
			agg = &fiscalSignAskVATLine{RateBP: b.RateBP}
			perRate[b.RateBP] = agg
		}
		agg.Net += b.Amount.Minor()
		agg.Tax += b.Tax.Minor()
		chargeTax = chargeTax.Add(b.Tax)
	}
	total := subtotal.Sub(in.SaleDiscount).Add(in.ServiceCharge)
	if !in.TaxInclusive {
		// Mirrors computeSaleTotals: the charge's tax rides on top exactly
		// like each line's own (inclusive already carries it inside the
		// charge amount).
		total = total.Add(taxTotal).Add(chargeTax)
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
		SaleID:        in.SaleID,
		SaleType:      defaultedSaleType(in.SaleType),
		Currency:      in.Currency,
		Total:         total.Minor(),
		TenderedAt:    now.Format(time.RFC3339),
		Payments:      payments,
		VATBreakdown:  vat,
		TaxInclusive:  in.TaxInclusive,
		SaleDiscount:  in.SaleDiscount.Minor(),
		ServiceCharge: in.ServiceCharge.Minor(),
	}
}

// recordFiscalTSEEvidence persists an approved answer's §6 KassenSichV
// evidence for a sale (ut-docs#585) — best-effort on failure, exactly like
// declareUnsignedFiscalSale's bookkeeping: the sale is already committed and
// must never be unwound over an evidence write failing. Idempotent at the
// repository level (first write wins), and a nil evidence (bare approval,
// pre-1.1.0 signer) is a no-op.
//
// A write failure here used to be log-only — silent to the operator and the
// journal, unlike the sibling declareUnsignedFiscalSale path (ut-docs#763).
// That asymmetry mattered: a sale that WAS signed but then lost its evidence
// is arguably worse than a cleanly-declared unsigned one, since nothing on
// the receipt or in the audit trail flagged it as needing attention. So a
// failure now gets the same two-part observability declareUnsignedFiscalSale
// gives its own failure: a journal marker and a Warnf into the Problems
// ring. Still never unwinds or blocks the sale — this is additive
// observability only.
func recordFiscalTSEEvidence(ctx context.Context, repo *data.POSRepo, saleID, actorID string, ev *fiscalTSEEvidence) {
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
		now := time.Now().UTC().Format(time.RFC3339)
		if auditErr := repo.InsertAudit(ctx, nil, actorID, "sale", saleID, "fiscal_evidence_persist_failed", map[string]any{
			"reason":    err.Error(),
			"failed_at": now,
		}, now, ""); auditErr != nil {
			log.Printf("fiscal signing: fiscal_evidence_persist_failed audit marker for sale %s failed: %v", saleID, auditErr)
		}
		logging.L().Warnf("fiscal signing: sale %s was signed but its §6 KassenSichV evidence failed to persist (%v) — no evidence will be shown on this sale's receipt; journaled for follow-up", saleID, err)
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
//	(c) operator alert — a Warn into the Problems ring (logging.Recent).
//
// That declaration is the sale's PERMANENT record, not a pending-recovery
// state (ut-docs#839, ADR-0056): TSE vendors do not permit belated signing
// of a completed transaction, so nothing re-attempts signing later and
// nothing ever upgrades an unsigned sale to signed after the fact.
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
// correspondingly never CLEARS it either.
func declareUnsignedFiscalSale(ctx context.Context, repo *data.POSRepo, saleID, actorID string, res fiscalSignResult) {
	now := time.Now().UTC().Format(time.RFC3339)
	knownOffline := res.Outcome == fiscalSignSkippedOffline
	cannotSign := res.Outcome == fiscalSignCannotSign

	// (a) Journal marker. Same InsertAudit shape as the unsigned_override
	// block in completeTender: best-effort after the fact, logged never
	// fatal. A cannot-sign refusal gets its OWN action name (ut-docs#835),
	// not the shared "unsigned_fiscal_signing" one — saleFiscalSigningGapKind
	// and both receipt render paths key off this to show wording that never
	// implies a connectivity outage for a sale that was never going to sign.
	action := fiscalSignGapActionSigning
	if cannotSign {
		action = fiscalSignGapActionCannotSign
	}
	if auditErr := repo.InsertAudit(ctx, nil, actorID, "sale", saleID, action, map[string]any{
		"reason":        res.Reason,
		"known_offline": knownOffline,
		"failed_at":     now,
	}, now, ""); auditErr != nil {
		log.Printf("fiscal signing: %s audit marker for sale %s failed: %v", action, saleID, auditErr)
	}

	// (c) Operator alert — Warn populates the Problems ring
	// (logging.remember), same surface warnIfStockNegative uses for its
	// per-sale condition; the cloud heartbeat's problems digest picks it up.
	if cannotSign {
		logging.L().Warnf("fiscal signing: sale %s completed UNSIGNED — could not be signed as presented (%s) — journaled, receipt carries a notice; the gap is permanent, signing is never re-attempted (fiscal.sign.ask proceed-and-declare, ADR-0044/ADR-0056, ut-docs#835/#839)", saleID, res.Reason)
	} else {
		logging.L().Warnf("fiscal signing: sale %s completed UNSIGNED (%s) — journaled, receipt carries an outage notice; the gap is permanent, signing is never re-attempted (fiscal.sign.ask proceed-and-declare, ADR-0044/ADR-0056, ut-docs#839)", saleID, res.Reason)
	}
}

// fiscalSignGapActionSigning / -CannotSign are the two audit actions
// declareUnsignedFiscalSale can write (ut-docs#835 split the original single
// "unsigned_fiscal_signing" action in two, by outcome kind) and the two
// saleFiscalSigningGapKind checks for, in this priority order — an entry
// never carries both, but checking cannot-sign first costs nothing on the
// far more common outage case (one extra indexed lookup only when it's
// actually present).
const (
	fiscalSignGapActionSigning    = "unsigned_fiscal_signing"
	fiscalSignGapActionCannotSign = "unsigned_fiscal_cannot_sign"
)

// saleFiscalSigningGapKind decides the receipt notice for both render paths
// (renderReceipt's flags in pos_api.go, the ESC/POS Meta lines in
// print_api.go): returns the audit action name of the sale's unresolved
// fiscal.sign.ask gap, or "" when there is none — either because the sale
// never had one, or because a fiscal_signing_resolved row shows a
// PRE-1.4.0 build's background retry already signed it. That row is purely
// historical (ut-docs#839, ADR-0056): no current code path writes it — the
// retry mechanism is gone — but the read-side check stays so an old sale's
// already-recorded recovery still renders clean on reprint. The returned
// action name is what the caller uses to pick wording: the original
// "unsigned_fiscal_signing" (outage/unproven wording) or ut-docs#835's
// "unsigned_fiscal_cannot_sign" (a signer's deterministic refusal — never
// worded as a connectivity problem, since it wasn't one). Errors degrade
// conservatively: can't read a marker → treated as absent (the authoritative
// record is the audit row itself, same policy as before); marker present but
// can't read the resolution → keep the gap, which was truthful at write time.
func saleFiscalSigningGapKind(ctx context.Context, repo *data.POSRepo, saleID string) string {
	for _, action := range []string{fiscalSignGapActionCannotSign, fiscalSignGapActionSigning} {
		hasGap, err := repo.HasAuditEntry(ctx, "sale", saleID, action)
		if err != nil || !hasGap {
			continue
		}
		resolved, err := repo.HasAuditEntry(ctx, "sale", saleID, "fiscal_signing_resolved")
		if err != nil || !resolved {
			return action
		}
		// This action's gap is resolved — but don't assume that means "no
		// gap at all" and short-circuit: the invariant that a sale never
		// carries both actions is enforced by declareUnsignedFiscalSale
		// being the sole writer of either, not by anything this read-side
		// helper can see. Keep checking the other action so a future
		// path that could violate that invariant (a hand-edited or
		// imported journal) fails safe — an unresolved gap under the
		// other action still surfaces its notice — rather than a resolved
		// one silently masking it.
		continue
	}
	return ""
}

// --- one-time migration: drop the pre-1.4.0 retry queue (ADR-0056) ---------

// dropStaleFiscalSignRetryQueue clears anything a pre-1.4.0 build's
// background retry queue persisted under common.KeyPendingFiscalSignRetries
// (ut-docs#839, ADR-0056): retry-signing is no longer performed, so a stale
// queue must not linger as if something will still happen to it — the sales
// it names stay permanently unsigned on their existing journal markers.
// Called once from init.go's boot sequence; idempotent, and the common case
// (nothing stored, or already migrated) costs one settings read and writes
// nothing. The "must not linger" claim is enforced end-to-end only because
// data.PerTillSettingPrefixes also excludes this key from admin sync
// (ut-docs#844) — otherwise a pre-1.4.0 primary could re-seed a replica's
// already-dropped queue on a later sync, since this migration only runs
// once at boot, not on every sync.
func dropStaleFiscalSignRetryQueue(ctx context.Context, d *common.Deps) {
	if d.Settings == nil {
		return
	}
	raw, ok, err := d.Settings.Get(ctx, common.KeyPendingFiscalSignRetries)
	if err != nil {
		logging.L().Warnf("fiscal signing: read stale retry queue for one-time drop: %v", err)
		return
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return
	}
	// Best-effort count for the log; a parse failure just means "drop it
	// anyway" — report the raw byte count instead.
	var entries []json.RawMessage
	if json.Unmarshal([]byte(raw), &entries) == nil {
		logging.L().Warnf("fiscal signing: dropping %d queued re-sign entries from a pre-1.4.0 build — background retry-signing was removed (ADR-0056, ut-docs#839); those sales stay permanently unsigned, exactly as their journal markers and receipts already declare", len(entries))
	} else {
		logging.L().Warnf("fiscal signing: dropping an unparseable pre-1.4.0 re-sign queue (%d bytes) — background retry-signing was removed (ADR-0056, ut-docs#839)", len(raw))
	}
	if err := d.Settings.Set(ctx, common.KeyPendingFiscalSignRetries, ""); err != nil {
		logging.L().Errorf("fiscal signing: clear stale retry queue: %v", err)
	}
}
