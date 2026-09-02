package pos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
)

// SaleInput captures the data needed to persist a sale (or return).
type SaleInput struct {
	SaleType     string // sale|return
	SaleID       string
	RegisterID   string
	CashierID    string
	CustomerID   string
	Currency     string
	TaxInclusive bool
	SaleDiscount money.Money // fixed discount (minor units) applied to whole sale
	// ServiceCharge is the ALREADY-COMPUTED till-set service charge amount
	// (minor units) to add to total -- deliberately a fixed amount, same
	// shape as SaleDiscount, NOT a rate: the caller (the live checkout
	// handler) computes it from the currently configured rate, and a
	// synced/replayed sale (internal/pages/sync_sales.go) passes the
	// ORIGINAL amount straight through from the journaled sale, exactly
	// like SaleDiscount already does -- a rate stored here instead would
	// silently recompute against whatever rate happens to be configured
	// at replay time, which is wrong for history. Unlike
	// PaymentInput.TipAmount (metadata, excluded from the sale total), a
	// service charge is revenue the customer owes and DOES participate in
	// netPayments' payment-sufficiency check.
	ServiceCharge money.Money
	// ServiceChargeTaxBasisBP (ADR-0061) is the flat tax rate for the
	// service charge, threaded through by the tender handler from an
	// installed country plugin's charge.policy.ask answer
	// (service_charge_tax_basis_bp). 0 — the value on every sale until a
	// country plugin actually answers, and always on a synced/replayed sale
	// — means the fail-closed default: the charge is taxed at the sale's own
	// per-line rates, apportioned by net line value
	// (ApportionServiceChargeTax). Deterministic either way, so a replayed
	// sale's computeSaleTotals reproduces the original totals exactly
	// without ever re-asking the policy hook (ADR-0061 Decision 4). NOTE:
	// the LAN-sync journal does not carry this field yet — that lands with
	// the follow-up card that makes ut-plugin-tax-{de,uk} actually answer
	// the hook, before any plugin can set it non-zero in production.
	ServiceChargeTaxBasisBP int
	// OrderType is the sale's SUMMARY order type (ADR-0073 Decision 1):
	// "" (all dine-in), pos.OrderTypeTakeaway (all takeaway) or
	// pos.OrderTypeMixed. CompleteSale DERIVES it from the lines'
	// own SaleLineInput.OrderType and ignores a caller-supplied value,
	// with one legacy exception: a "takeaway" header whose lines all carry
	// no order type (a pre-ADR-0073 peer's journal, or an old held sale)
	// means every line was takeaway, so those lines are filled in first.
	OrderType string
	// TableID (ut-docs#820, ADR-0054) is the dining table this sale was
	// served at, or "" when none was assigned -- whatever the checkout's
	// basket already carries (Service.TableID), persisted so a completed
	// sale's receipt/journal/kitchen ticket can show it after the fact,
	// same convention as OrderType above.
	TableID        string
	Lines          []SaleLineInput
	Payments       []PaymentInput
	OriginalSaleID string // for returns; creates sale_links entry when set
	Note           string
	ReceiptNo      string
	ActorID        string
	// AllowNegativeInventory, when false, makes CompleteSale reject any sale
	// line that would take THIS till's local stock copy negative. It is a
	// PRIMARY-only policy (ADR-0036, amending ADR-0011 §3 — ut-docs#404):
	// stock has exactly one owner, the primary/back-office till, so the
	// shop's pos.allow_negative_inventory setting governs only the primary's
	// own direct sales. A replica's direct-sale paths ALWAYS pass true (its
	// local figure is a cache the 30s sync tick cannot keep current, so the
	// gate would pass/fail against legitimately stale data), and journal
	// replay on the primary ALWAYS passes true (the remote sale already
	// happened) — the till that owns stock then surfaces any resulting
	// negative level as a back-office Problem instead of blocking a sale.
	//
	// ADR-0036's own consequences note says the replica-side gate "should
	// be removed [on that path], not just skipped" so a future change
	// can't accidentally restore a stale-data gate. This field stays and
	// is force-true instead, deliberately: it is ALSO the primary's own
	// policy switch (pos.allow_negative_inventory), so splitting "gate
	// disabled by shop policy" from "gate bypassed because this till
	// doesn't own the number" into two fields is a real field/callers
	// refactor across every CompleteSale caller, not a one-line change —
	// out of proportion to this fix. The callers that force it true
	// (internal/pages/pos_api.go, self_order_shop.go on a replica;
	// sync_sales.go's applyJournal always) each carry an explicit ADR-0036
	// comment of their own, so the "why" is visible at every call site
	// even though the field itself wasn't split.
	AllowNegativeInventory bool
	// AllowVoucherOverdraft (ut-docs#1053), when true, lets a tracked
	// voucher redemption debit past the voucher's balance (the balance goes
	// negative) instead of failing ErrVoucherInsufficientBalance. It is set
	// true ONLY by the LAN-sync journal replay path
	// (internal/pages/sync_sales.go's applyJournal) — the exact
	// AllowNegativeInventory precedent above: on a genuine offline
	// double-spend of the same multi-purpose voucher across two tills, the
	// money already moved at the remote till, so the replay must record the
	// sale and surface the overdraft as a back-office Problem rather than
	// poison that replica's journal forever. A voided/nonexistent voucher
	// still hard-rejects even with this set (see
	// data.DebitVoucherForRedemption). Every direct-sale path leaves it
	// false — the live balance gate is unchanged.
	AllowVoucherOverdraft bool
	Offline               bool
	// VoucherIssues (ut-docs#1008) are multi-purpose vouchers sold in this
	// sale. A voucher is NOT an article: it never becomes a sale_lines row
	// (the CHECK constraint there requires a catalog identity, and a fake
	// catalog item would count the issue as article revenue — the exact bug
	// this field exists to avoid). Each issue is a 0% VAT liability: its
	// amount is EXCLUDED from subtotal/tax_total (and therefore from every
	// sale_lines-derived figure — departments, per-rate VAT bands) but
	// INCLUDED in total, since the customer pays for it. Persisted as one
	// vouchers row + one voucher_transactions 'issue' row in the same
	// transaction as the sale, same precedent as sale_charges (ADR-0062).
	// The LAN-sync journal carries these since contract 1.3.0
	// (ut-docs#1053): data.SaleDetail.VoucherIssues rides the wire and
	// applyJournal reconstructs this field on replay.
	VoucherIssues []VoucherIssueInput
}

// VoucherIssueInput is one voucher sold in a sale (ut-docs#1008).
type VoucherIssueInput struct {
	// VoucherID is the stable voucher identifier/code. Empty means
	// CompleteSale generates one (a uuid); a caller may supply its own
	// (e.g. a preprinted card's code), which must be unique.
	VoucherID string
	// HolderLabel optionally names who the voucher is for (free text).
	HolderLabel string
	// Amount is the voucher's face value (minor units), > 0. It becomes
	// both original_amount and the opening balance (the liability).
	Amount money.Money
}

type SaleLineInput struct {
	ItemID             string
	VariantID          string
	SKU                string
	Barcode            string
	Name               string
	Qty                float64     // REAL; supports weighed items
	UnitPrice          money.Money // minor units, before discount; already includes any modifier price deltas (ADR-0020)
	TaxRateBasisPoints int
	LineDiscount       money.Money // fixed minor units
	LocationID         string      // stock movement location
	Modifiers          []data.SelectedModifier
	// OrderType (ut-docs#1181, ADR-0073): this line's own consumption
	// mode, "" (dine-in) or OrderTypeTakeaway. Anything else (including
	// OrderTypeMixed) is clamped to dine-in at the CompleteSale choke point.
	// TaxRateBasisPoints is still the money authority -- the tender path
	// resolved it through the tax plugin for THIS mode -- this field is the
	// provenance that travels with it (receipt, kitchen, journal, sync,
	// refund, sale.completed).
	OrderType string
}

// json tags (independent review, ut-docs#543): PaymentInput is never
// JSON-decoded (the tender handler decodes into its own local request
// struct and maps fields across), only ever encoded -- once, in the
// /api/pos/tender JSON response. It shipped with no tags at all, so it
// serialised as bare Go PascalCase, against universal-till/CLAUDE.md's
// "JSON snake_case" rule and out of step with the identical wire
// vocabulary data.SaleDetailPayment already uses correctly.
type PaymentInput struct {
	MethodID    string      `json:"method_id"`
	Amount      money.Money `json:"amount"`
	Currency    string      `json:"currency,omitempty"`
	Reference   string      `json:"reference,omitempty"`
	ChangeGiven money.Money `json:"change_given"`
	// TipAmount is gratuity captured alongside a card-terminal payment
	// (docs/germany-pos-parity-backlog.md, "Tips: SumUp reader -> till
	// auto-sync"). It is metadata only: NOT part of Amount's coverage of
	// the sale total, and does not affect netPayments/CompleteSale's
	// payment-sufficiency check. Zero for tenders with no tip (e.g. cash).
	TipAmount money.Money `json:"tip_amount"`
	// TipRecipient (ADR-0061 Decision 3) is whose money the tip is for tax
	// purposes: TipRecipientEmployee or TipRecipientBusiness. Persisted per
	// payment so a report built later (ut-docs#964) reads the recipient as
	// it actually was at capture time, never recomputed from a policy that
	// may have since changed. Empty defaults to employee at persistence —
	// the one default every researched market agrees on; the tender path
	// consults charge.policy.ask's tip_default_recipient before this ever
	// applies. Any other value is rejected (validate all external input).
	TipRecipient string `json:"tip_recipient,omitempty"`

	// Card-present reconciliation fields (ut-docs#543) -- optional,
	// provider-agnostic metadata a locally-attached card terminal (e.g. a
	// future ZVT integration, ut-docs#515) supplies. All empty for every
	// payment method today (cash, Stripe, SumUp, QR-pay, demo).
	//
	// MaskedPAN must NEVER be a full card number -- scheme + last 4
	// digits only (e.g. "VISA •••• 4242"). CompleteSale rejects anything
	// that looks like an unmasked PAN before it ever reaches persistence;
	// masking is the caller's responsibility before this field is set.
	MaskedPAN  string `json:"masked_pan,omitempty"`
	AuthCode   string `json:"auth_code,omitempty"` // terminal's auth/approval code
	TerminalID string `json:"terminal_id,omitempty"`
	TraceID    string `json:"trace_id,omitempty"` // terminal transaction/trace ID

	// VoucherID (ut-docs#1008) marks this payment as a TRACKED voucher
	// redemption: it must ride MethodID "voucher" (the pre-existing default
	// payment method), and CompleteSale then validates the vouchers row's
	// balance covers Amount (overspend rejected — no partial split logic),
	// debits it, and records a voucher_transactions 'redemption' row
	// alongside the payments row. Empty keeps the generic, untracked
	// 'voucher' payment behavior exactly as it always was — redemption
	// tracking is recorded separately from that legacy payment type, and
	// historical voucher payments are deliberately not reinterpreted.
	// Redemption changes NOTHING about how the goods being paid for are
	// taxed — only the payment method differs.
	VoucherID string `json:"voucher_id,omitempty"`
}

// maxMaskedPANDigits bounds how many ASCII digits a MaskedPAN value may
// contain. A properly masked display (scheme + last 4 digits, e.g.
// "VISA •••• 4242") never has more than 4 real digits -- everything else
// is mask characters or scheme text. A real, unmasked PAN (12-19 digits,
// however it's grouped -- "4242424242424242" or "4242 4242 4242 4242")
// always has more, so this catches it regardless of grouping.
const maxMaskedPANDigits = 4

// validateMaskedPAN rejects a PaymentInput.MaskedPAN value that looks like
// an unmasked PAN. This is the persistence boundary (ut-docs#543): masking
// must happen here, not just at render time, since GetSaleDetail and the
// receipt template both trust whatever was stored.
func validateMaskedPAN(s string) error {
	digits := 0
	// unicode.IsDigit, not a bare '0'-'9' range: this product ships fa/ar
	// locales, so a full PAN written in Arabic-Indic or fullwidth digits
	// is a real input shape, not a theoretical one -- an ASCII-only check
	// let it straight through (independent review, ut-docs#543).
	for _, r := range s {
		if unicode.IsDigit(r) {
			digits++
			if digits > maxMaskedPANDigits {
				return fmt.Errorf("masked_pan must show at most the last %d digits (got a value that looks like an unmasked PAN)", maxMaskedPANDigits)
			}
		}
	}
	return nil
}

// MaxVoucherIssueAmount is the sanity ceiling for a single voucher's face
// value (minor units): 1,000,000.00 — the same 1,000,000-major-units ceiling
// the catalog cost/price handlers apply to external money input
// (universaltill/ut-docs#276), far beyond any real gift voucher yet small
// enough that no realistic count of vouchers summed into a sale total can
// approach int64 overflow (ut-docs#1008 review, major F3: two unbounded
// amounts near 2^62 wrapped `total` negative, trivially passing the
// payment-coverage check). Enforced both at the API boundary
// (internal/pages/pos_api.go) and in computeSaleTotals itself — the API is
// not CompleteSale's only caller.
const MaxVoucherIssueAmount money.Money = 100_000_000

// MaxVoucherIssuesPerSale (ut-docs#1052, follow-up from the ut-docs#1008
// review) caps how many vouchers a single sale may issue. MaxVoucherIssueAmount
// alone bounds each voucher's face value but not how many get summed
// together in computeSaleTotals's accumulation loop -- money.Money.Add is
// plain unchecked int64 arithmetic. Wrapping int64 at the ceiling needs
// ~9.2×10¹⁰ vouchers in one request (a multi-terabyte JSON body that would
// exhaust memory in json.Decode long before the running total could wrap),
// so the count was never truly unbounded, only incidentally so (verified by
// direct probe in the 2026-08-25 review round). This cap -- and the explicit
// overflow check in the loop below -- makes the guarantee asserted rather
// than accidental, at a threshold no real till gets near.
const MaxVoucherIssuesPerSale = 50

// ErrVoucherOvertender rejects a tracked voucher redemption whose amount
// exceeds what the sale still needs (ut-docs#1008 review, major F4): a
// voucher payment can never give change or carry a tip (netPayments), so
// accepting an over-tender would silently confiscate the excess from the
// voucher's balance instead of refusing. Exported so the tender handler's
// classifyTenderError can map it to its own toast via errors.Is.
var ErrVoucherOvertender = errors.New("voucher redemption exceeds the amount the sale still needs")

const receiptRetryLimit = 5

var errReceiptConflictRetry = errors.New("receipt_conflict_retry")

const (
	syncStatusQueued = "queued"
	syncStatusSynced = "synced"
)

var receiptAllocator = func(ctx context.Context, tx *sql.Tx, repo *data.POSRepo) (string, error) {
	return repo.NextReceiptNo(ctx, tx)
}

func computeSaleTotals(in SaleInput) (subtotal, taxTotal, serviceCharge, voucherIssueTotal, total money.Money, err error) {
	// vatLines mirrors each line's recorded tax rate/gross/tax so taxTotal
	// below can be derived from the SAME VATBandsForSale apportionment the
	// invoice VAT table and day-close bands already use (eod_tax_bands.go,
	// invoice_page.go), rather than a second, independent accumulation that
	// can (and did, ut-docs#1035) silently drift from it: a flat per-line
	// sum here never reduced for in.SaleDiscount, while VATBandsForSale
	// correctly re-derives Tax per band for inclusive-priced sales.
	//
	// Known historical gap, deliberately not migrated (ut-docs#1114): a
	// sale row persisted by a PRE-#1035 build still carries the old flat
	// (undiscounted) tax_total for this one shape, so
	// sum(VATBandsForSale(...).Tax) for that row no longer equals its
	// stored tax_total once re-read by this fixed build — eod_tax_bands.go
	// documents the same gap at the identity it breaks. Not fixed by a
	// background UPDATE: for a German shop, a TSE-signed sale row is meant
	// to stay immutable together with its signed record (GoBD), so
	// silently rewriting a persisted tax_total after the fact is itself in
	// tension with that — arguably worse than the documented gap. Current
	// product-owner call (2026-08-27, no real shop live yet): leave
	// historical rows as-is unless concrete evidence surfaces that a real
	// filed VAT return was affected. Re-read ut-docs#1114 before adding a
	// reconciliation pass here.
	vatLines := make([]VATLine, 0, len(in.Lines))
	for _, l := range in.Lines {
		if err := validateLine(l); err != nil {
			return 0, 0, 0, 0, 0, err
		}
		lineBase := AmountForQuantity(l.UnitPrice, l.Qty)
		if l.LineDiscount.IsNegative() || l.LineDiscount > lineBase {
			return 0, 0, 0, 0, 0, fmt.Errorf("invalid line discount for item %s", l.ItemID)
		}
		lineNet := lineBase.Sub(l.LineDiscount)
		// The second return is the line's GROSS amount either way (net+tax
		// exclusive, unchanged-i.e.-already-gross inclusive) -- exactly what
		// VATLine.LineTotal wants (see vat_breakdown.go's VATLine doc).
		lineTax, lineGross := ComputeTaxBasisPoints(lineNet, l.TaxRateBasisPoints, in.TaxInclusive)
		subtotal = subtotal.Add(lineNet)
		vatLines = append(vatLines, VATLine{RateBP: l.TaxRateBasisPoints, LineTotal: lineGross.Minor(), TaxAmount: lineTax.Minor()})
	}
	// serviceCharge=0 here deliberately: VATBandsForSale's discount
	// apportionment never touches service-charge tax (it's added to bands
	// in a separate step, after this one, in VATBandsForSale itself), so
	// leaving it out keeps this call scoped to exactly the line+discount
	// figure and orthogonal to the chargeTax fold below -- no double count.
	for _, b := range VATBandsForSale(vatLines, in.SaleDiscount.Minor(), in.TaxInclusive, 0, 0) {
		taxTotal = taxTotal.Add(money.FromMinor(b.Tax))
	}
	if taxTotal.IsNegative() {
		// An over-discount (SaleDiscount exceeding subtotal) can drive a
		// band's Gross negative -- total is already floored below for the
		// same reason; mirror that here so a persisted sale never carries a
		// negative tax_total.
		taxTotal = 0
	}
	discountedSubtotal := subtotal.Sub(in.SaleDiscount)
	if in.ServiceCharge.IsNegative() {
		return 0, 0, 0, 0, 0, fmt.Errorf("service charge must be >= 0")
	}
	serviceCharge = in.ServiceCharge
	// ADR-0061 Decision 2: the service charge is ALWAYS taxed — at a
	// plugin-answered flat basis when one was threaded through, else
	// apportioned across the sale's own per-line rate bands (the fail-closed
	// default; no plugin subsystem exists in this package, so nothing here
	// can be asked — the untaxed path is unreachable by construction).
	// Inclusive pricing embeds the charge's tax inside the charge amount
	// (taxTotal declares it, total is unchanged); exclusive adds it on top
	// via the taxTotal fold below — the same split the lines themselves get.
	chargeTax := ServiceChargeTax(serviceCharge, ChargeTaxLinesFromSale(in.Lines), in.TaxInclusive, in.ServiceChargeTaxBasisBP)
	taxTotal = taxTotal.Add(chargeTax)
	total = discountedSubtotal.Add(serviceCharge)
	if !in.TaxInclusive {
		total = total.Add(taxTotal)
	}
	if total.IsNegative() {
		total = 0
	}
	// Voucher issues (ut-docs#1008): a 0% liability the customer pays for —
	// folded into total AFTER the negative clamp (a sale discount must never
	// eat into a voucher's face value: the full amount is owed to the future
	// bearer), and NEVER into subtotal/taxTotal (it is not revenue and not a
	// taxable supply; VAT arises only at redemption, ut-docs#1008). The
	// summed face value is returned separately so CompleteSale can persist
	// it on the sale header (sales.voucher_issue_total, migration 069) —
	// InferTaxInclusive needs it on the other side of its identity.
	// ut-docs#1052: the per-voucher ceiling below bounds each amount but
	// not the count, so cap that too rather than lean on the incidental
	// memory-exhaustion floor described on MaxVoucherIssuesPerSale.
	if len(in.VoucherIssues) > MaxVoucherIssuesPerSale {
		return 0, 0, 0, 0, 0, fmt.Errorf("sale issues %d vouchers, exceeding the maximum of %d per sale", len(in.VoucherIssues), MaxVoucherIssuesPerSale)
	}
	const maxMoney = money.Money(math.MaxInt64)
	for i, v := range in.VoucherIssues {
		if !v.Amount.IsPositive() {
			return 0, 0, 0, 0, 0, fmt.Errorf("voucher issue %d: amount must be > 0", i+1)
		}
		// Defense in depth for the int64-overflow coverage bypass (review
		// F3) — the same ceiling pos_api.go enforces at the HTTP boundary,
		// re-checked here because the API is not the only CompleteSale
		// caller (self-order, future plugin paths, journal replay).
		if v.Amount > MaxVoucherIssueAmount {
			return 0, 0, 0, 0, 0, fmt.Errorf("voucher issue %d: amount %d exceeds the maximum of %d minor units", i+1, v.Amount.Minor(), MaxVoucherIssueAmount.Minor())
		}
		// ut-docs#1052: assert the running-total guarantee explicitly
		// instead of relying on the count/amount ceilings above making it
		// merely incidental -- this can't actually trip given those
		// ceilings (50 * 100,000,000.00 is nowhere near 2^63-1), but it
		// means the invariant is checked, not assumed.
		if voucherIssueTotal > maxMoney-v.Amount || total > maxMoney-v.Amount {
			return 0, 0, 0, 0, 0, fmt.Errorf("voucher issue %d: running total would overflow", i+1)
		}
		voucherIssueTotal = voucherIssueTotal.Add(v.Amount)
		total = total.Add(v.Amount)
	}
	return subtotal, taxTotal, serviceCharge, voucherIssueTotal, total, nil
}

// summarizeLineInputs is SummarizeOrderType over SaleLineInputs (no lines
// -> "" -- a persisted sale always has lines, validateLine rejects none).
func summarizeLineInputs(lines []SaleLineInput) string {
	dineIn, takeaway := false, false
	for _, l := range lines {
		if NormalizeLineOrderType(l.OrderType) == OrderTypeTakeaway {
			takeaway = true
		} else {
			dineIn = true
		}
	}
	switch {
	case dineIn && takeaway:
		return OrderTypeMixed
	case takeaway:
		return OrderTypeTakeaway
	default:
		return ""
	}
}

// netPayments validates the payment list and returns the sum that must cover
// `total` (the sale's computed total, passed in so a tracked voucher
// redemption can be capped at what the sale still needs at its point in the
// list — review F4).
func netPayments(payments []PaymentInput, total money.Money) (money.Money, error) {
	var sum money.Money
	if len(payments) == 0 {
		return 0, errors.New("sale requires at least one payment")
	}
	for i, p := range payments {
		if p.MethodID == "" {
			return 0, fmt.Errorf("payment %d missing method", i+1)
		}
		if !p.Amount.IsPositive() {
			return 0, fmt.Errorf("payment %d amount must be > 0", i+1)
		}
		if p.ChangeGiven.IsNegative() {
			return 0, fmt.Errorf("payment %d change must be >= 0", i+1)
		}
		if p.ChangeGiven > p.Amount {
			return 0, fmt.Errorf("payment %d change cannot exceed amount", i+1)
		}
		if p.TipAmount.IsNegative() {
			return 0, fmt.Errorf("payment %d tip must be >= 0", i+1)
		}
		switch p.TipRecipient {
		case "", TipRecipientEmployee, TipRecipientBusiness:
		default:
			return 0, fmt.Errorf("payment %d tip recipient must be %q or %q", i+1, TipRecipientEmployee, TipRecipientBusiness)
		}
		if p.VoucherID != "" {
			// Tracked voucher redemption (ut-docs#1008): rides the existing
			// 'voucher' payment method only, and — fail-closed, this card
			// defers any split/change semantics for vouchers — never gives
			// change or carries a tip: the debited amount must be exactly
			// what the payments row records.
			if strings.ToLower(strings.TrimSpace(p.MethodID)) != "voucher" {
				return 0, fmt.Errorf("payment %d: voucher_id requires the voucher payment method", i+1)
			}
			if p.ChangeGiven.IsPositive() {
				return 0, fmt.Errorf("payment %d: a voucher redemption cannot give change", i+1)
			}
			if p.TipAmount.IsPositive() {
				return 0, fmt.Errorf("payment %d: a voucher redemption cannot carry a tip", i+1)
			}
			// Same identifier bounds the issue path and GET /api/vouchers/{id}
			// enforce (review minor F6) — one shared rule for every surface a
			// voucher id enters through.
			if err := validateVoucherID(p.VoucherID); err != nil {
				return 0, fmt.Errorf("payment %d: %w", i+1, err)
			}
			// Over-tender cap (review F4): with change and tips forbidden
			// above, any excess over what the sale still needs at this point
			// would be silently drained from the voucher's balance and lost
			// to the customer — refuse instead. `sum` is the coverage
			// contributed by the payments before this one.
			if outstanding := total.Sub(sum); p.Amount > outstanding {
				return 0, fmt.Errorf("payment %d (amount %d, outstanding %d): %w", i+1, p.Amount.Minor(), outstanding.Minor(), ErrVoucherOvertender)
			}
		}
		// Tip is intentionally excluded from the sum that must cover the
		// sale total -- it never offsets or inflates payment coverage.
		sum = sum.Add(p.Amount.Sub(p.ChangeGiven))
	}
	return sum, nil
}

func deriveTenderType(payments []PaymentInput) string {
	if len(payments) == 0 {
		return "unknown"
	}
	method := strings.ToLower(strings.TrimSpace(payments[0].MethodID))
	if method == "" {
		method = "unknown"
	}
	for i := 1; i < len(payments); i++ {
		next := strings.ToLower(strings.TrimSpace(payments[i].MethodID))
		if next != method {
			return "split"
		}
	}
	return method
}

func isReceiptConflictErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "receipt_no") && strings.Contains(strings.ToLower(msg), "unique")
}

// CompleteSale persists a sale (or return) with lines, payments, stock movements, discounts, and optional sale link.
// It enforces payment coverage, FK constraints, and uses a single transaction for integrity.
func CompleteSale(ctx context.Context, sqlDB *sql.DB, in SaleInput) (string, error) {
	repo := data.NewPOSRepo(sqlDB)
	// A voucher-only sale (ut-docs#1008) is legitimate — a customer buying
	// just a gift voucher rings no article line at all.
	if len(in.Lines) == 0 && len(in.VoucherIssues) == 0 {
		return "", errors.New("sale requires at least one line or voucher issue")
	}
	if in.SaleType == "" {
		in.SaleType = "sale"
	}
	// Voucher flows are sale-only in this card (ut-docs#1008 non-goals):
	// issuing a voucher on a return, or refunding onto a tracked voucher,
	// are undesigned — fail closed rather than invent semantics here.
	if in.SaleType == "return" {
		if len(in.VoucherIssues) > 0 {
			return "", errors.New("a return cannot issue vouchers")
		}
		for i, p := range in.Payments {
			if p.VoucherID != "" {
				return "", fmt.Errorf("payment %d: a return cannot redeem a tracked voucher", i+1)
			}
		}
	}
	// Normalize/generate voucher identifiers ONCE, before the receipt retry
	// loop below — a retried transaction must issue the SAME voucher codes
	// it would have on the first attempt.
	issuedIDs := make(map[string]bool, len(in.VoucherIssues))
	for i := range in.VoucherIssues {
		id := strings.TrimSpace(in.VoucherIssues[i].VoucherID)
		if id == "" {
			id = uuid.NewString()
		}
		if err := validateVoucherID(id); err != nil {
			return "", fmt.Errorf("voucher issue %d: %w", i+1, err)
		}
		in.VoucherIssues[i].VoucherID = id
		in.VoucherIssues[i].HolderLabel = strings.TrimSpace(in.VoucherIssues[i].HolderLabel)
		issuedIDs[id] = true
	}
	// Normalize each payment's redemption identifier the same way (review
	// minor F6 — the redemption path used to accept what the issue path
	// rejects), and refuse redeeming a voucher this SAME sale is issuing
	// (review major F5): CompleteSale writes the issue rows before the
	// payment loop runs, so without this guard a self-referencing sale
	// fabricated an issue+redemption pair — inflating both GUTSCHEINE
	// counters and Gross — with no real prior liability behind it. Same
	// fail-closed style as the SaleType == "return" guard above.
	for i := range in.Payments {
		vid := strings.TrimSpace(in.Payments[i].VoucherID)
		in.Payments[i].VoucherID = vid
		if vid == "" {
			continue
		}
		if issuedIDs[vid] {
			return "", fmt.Errorf("payment %d: voucher %q is issued in this same sale and cannot also pay for it", i+1, vid)
		}
	}
	if in.Currency == "" {
		in.Currency = "GBP"
	}
	// ADR-0073: normalize every LINE's order type at this one choke point
	// (cashier, kiosk, sync replay, refund, return all pass through here),
	// then DERIVE the header summary from the lines. A caller-supplied
	// header is only consulted for the legacy case -- "takeaway" with no
	// line carrying a value -- where it is the only record of the lines'
	// mode (pre-1.5.0 LAN journal, pre-ADR-0073 held sale). Anything else a
	// caller wrote in the header is discarded, so a mixed sale can never be
	// labelled "takeaway" and "mixed" can never land on a line.
	anyLineTyped := false
	for i := range in.Lines {
		in.Lines[i].OrderType = NormalizeLineOrderType(in.Lines[i].OrderType)
		if in.Lines[i].OrderType != "" {
			anyLineTyped = true
		}
	}
	if !anyLineTyped && in.OrderType == OrderTypeTakeaway {
		for i := range in.Lines {
			in.Lines[i].OrderType = OrderTypeTakeaway
		}
	}
	// A legacy RETURN (second-round review, ut-docs#1181): the pre-ADR-0073
	// refund path never set a header on a return, so an old-build peer's
	// journaled return arrives with header "" AND untyped lines — the
	// takeaway rule above cannot fire. Inherit each line's mode from the
	// ORIGINAL sale's persisted lines (matched the same way the refund
	// pool is keyed: item, variant, unit price) — the runtime twin of
	// migration 078's sale_links backfill — or the whole refund pool would
	// be keyed per mode while this return's lines sit under dine-in, and a
	// fully-refunded takeaway unit could be refunded again.
	if !anyLineTyped && in.SaleType == "return" && in.OriginalSaleID != "" {
		if orig, err := repo.ListSaleLineSnapshots(ctx, in.OriginalSaleID); err == nil && len(orig) > 0 {
			modeFor := map[string]string{}
			for _, o := range orig {
				if o.OrderType == OrderTypeTakeaway {
					modeFor[o.ItemID+"|"+o.VariantID+"|"+strconv.FormatInt(o.UnitPrice, 10)] = OrderTypeTakeaway
				}
			}
			for i := range in.Lines {
				l := in.Lines[i]
				k := l.ItemID + "|" + l.VariantID + "|" + strconv.FormatInt(l.UnitPrice.Minor(), 10)
				if l.VariantID != "" {
					k = "|" + l.VariantID + "|" + strconv.FormatInt(l.UnitPrice.Minor(), 10)
				}
				if m, ok := modeFor[k]; ok {
					in.Lines[i].OrderType = m
				}
			}
		}
	}
	in.OrderType = summarizeLineInputs(in.Lines)
	// ut-docs#744: a variant resolved by barcode carries BOTH ItemID and
	// VariantID upstream (ui.PriceResolverAdapter deliberately keeps ItemID
	// alongside VariantID on the live BasketLine -- internal/pages/tax_hook.go's
	// tax.rate.ask payload still needs it there for a variant line). But a
	// sale line's *persisted* identity must be exactly one of the two, same
	// as the sale_lines/inventory/stock_movements CHECK constraints already
	// require -- so normalize here, at the one choke point every caller
	// (cashier, kiosk, sync replay, refund, return) goes through, rather
	// than trusting every SaleLineInput{} construction site to get this
	// right individually. VariantID wins: it's the more specific identity.
	//
	// This mutates in.Lines[i] in place, and since in.Lines is a slice,
	// that mutation is visible to the caller's own backing array after
	// CompleteSale returns -- deliberately: publishStockAdjustedForSale
	// (pos_api.go) and warnIfStockNegative (sync_sales.go) both read
	// l.ItemID/l.VariantID from the same Lines slice post-completion, and
	// need the cleared form (CurrentQty's own query only matches when
	// exactly one is set, same as the CHECK constraints above). A caller
	// that passed a defensive copy of Lines would silently lose this and
	// read the wrong inventory row -- same fragility class already called
	// out for PaymentInput.TipAmount aliasing in pos_api.go.
	for i := range in.Lines {
		if in.Lines[i].VariantID != "" {
			in.Lines[i].ItemID = ""
		}
	}
	subtotal, taxTotal, serviceCharge, voucherIssueTotal, total, err := computeSaleTotals(in)
	if err != nil {
		return "", err
	}
	netPaid, err := netPayments(in.Payments, total)
	if err != nil {
		return "", err
	}
	if netPaid < total {
		return "", fmt.Errorf("payments (%d) do not cover total (%d)", netPaid, total)
	}
	saleID := in.SaleID
	if saleID == "" {
		saleID = uuid.NewString()
	}

	providedReceipt := in.ReceiptNo
	tenderType := deriveTenderType(in.Payments)

	for attempt := 0; attempt < receiptRetryLimit; attempt++ {
		in.ReceiptNo = providedReceipt

		err = db.WithTx(ctx, sqlDB, func(tx *sql.Tx) error {
			// ut-docs#1318: ONE batched inventory read for every line's stock
			// key, replacing the old per-line CurrentQty loop. The map also
			// feeds RecordStockMovementsBatch's has-row/needs-row split below,
			// so it is read even when the stock check itself is bypassed.
			stockKeys := make([]data.StockKey, 0, len(in.Lines))
			for _, l := range in.Lines {
				stockKeys = append(stockKeys, data.StockKey{LocationID: l.LocationID, ItemID: l.ItemID, VariantID: l.VariantID})
			}
			currentQtys, err := repo.CurrentQtyBatch(ctx, tx, stockKeys)
			if err != nil {
				return err
			}
			if !in.AllowNegativeInventory {
				// Same semantics as the old loop, deliberately: every line is
				// checked independently against the same pre-sale quantity (a
				// key absent from the map reads as 0, CurrentQty's found=false
				// meaning). Two lines selling the same item are NOT checked
				// against a running total — pre-existing quirk, preserved; the
				// batched check must not be stricter than the loop it replaced.
				for _, l := range in.Lines {
					cur := currentQtys[data.StockKey{LocationID: l.LocationID, ItemID: l.ItemID, VariantID: l.VariantID}]
					qtyDelta := l.Qty
					if in.SaleType == "sale" {
						qtyDelta = -qtyDelta
					}
					if cur+qtyDelta < 0 {
						return fmt.Errorf("insufficient stock for item %s at location %s (have %.2f, need %.2f)", valueOrDefault(l.ItemID, l.VariantID), l.LocationID, cur, l.Qty)
					}
				}
			}

			receiptNo := in.ReceiptNo
			now := time.Now().UTC().Format(time.RFC3339)
			syncStatus := syncStatusSynced
			syncNextAttemptAt := ""
			if in.Offline {
				syncStatus = syncStatusQueued
				syncNextAttemptAt = now
			}
			if receiptNo == "" {
				var err error
				receiptNo, err = receiptAllocator(ctx, tx, repo)
				if err != nil {
					return err
				}
			}
			if err := repo.InsertSale(ctx, tx, data.InsertSaleParams{
				SaleID:                  saleID,
				ReceiptNo:               receiptNo,
				SaleType:                in.SaleType,
				RegisterID:              in.RegisterID,
				CashierID:               in.CashierID,
				CustomerID:              in.CustomerID,
				Currency:                in.Currency,
				Subtotal:                subtotal.Minor(),
				DiscountTotal:           in.SaleDiscount.Minor(),
				TaxTotal:                taxTotal.Minor(),
				Total:                   total.Minor(),
				ServiceCharge:           serviceCharge.Minor(),
				ServiceChargeTaxBasisBP: in.ServiceChargeTaxBasisBP,
				VoucherIssueTotal:       voucherIssueTotal.Minor(),
				Note:                    in.Note,
				CreatedAt:               now,
				TenderType:              tenderType,
				OrderType:               in.OrderType,
				TableID:                 in.TableID,
				Offline:                 in.Offline,
				SyncStatus:              syncStatus,
				SyncAttempts:            0,
				SyncNextAttemptAt:       syncNextAttemptAt,
				SyncLastError:           "",
			}); err != nil {
				if in.ReceiptNo == "" && isReceiptConflictErr(err) {
					return errReceiptConflictRetry
				}
				return err
			}
			in.ReceiptNo = receiptNo

			// ut-docs#1318: build every per-line row up front (line IDs are
			// caller-generated, same as before), then land them in a handful
			// of batched statements instead of ~5 statements per line. The
			// sale-level discount rides in the same discounts batch, as its
			// first row, so its relative order in sale_discounts is unchanged.
			lineRows := make([]data.SaleLineRow, 0, len(in.Lines))
			var modifierRows []data.SaleLineModifierRow
			var discountRows []data.SaleDiscountRow
			if in.SaleDiscount.IsPositive() {
				discountRows = append(discountRows, data.SaleDiscountRow{
					ID:     uuid.NewString(),
					SaleID: saleID,
					Type:   "fixed",
					Value:  in.SaleDiscount.Minor(),
					Amount: in.SaleDiscount.Minor(),
					Reason: "sale_discount",
				})
			}
			movements := make([]data.StockMovementInput, 0, len(in.Lines))
			for i, l := range in.Lines {
				lineID := uuid.NewString()
				lineBase := AmountForQuantity(l.UnitPrice, l.Qty)
				lineNet := lineBase.Sub(l.LineDiscount)
				lineTax, _ := ComputeTaxBasisPoints(lineNet, l.TaxRateBasisPoints, in.TaxInclusive)
				// Inclusive pricing: the ticket price already contains the
				// tax — after-tax IS the price, before-tax is net of it.
				// Exclusive: tax goes on top. (Was unconditionally on-top:
				// inflated total_after_tax + TopItems revenue for inclusive
				// shops; repaired by migration 012.)
				totalBeforeTax := lineNet
				totalAfterTax := lineNet.Add(lineTax)
				if in.TaxInclusive {
					totalBeforeTax = lineNet.Sub(lineTax)
					totalAfterTax = lineNet
				}

				lineRows = append(lineRows, data.SaleLineRow{
					ID:             lineID,
					SaleID:         saleID,
					LineNo:         i + 1,
					ItemID:         l.ItemID,
					VariantID:      l.VariantID,
					Name:           l.Name,
					SKU:            l.SKU,
					Barcode:        l.Barcode,
					Qty:            l.Qty,
					UnitPrice:      l.UnitPrice.Minor(),
					LineDiscount:   l.LineDiscount.Minor(),
					TaxRateBP:      l.TaxRateBasisPoints,
					TaxAmount:      lineTax.Minor(),
					TotalBeforeTax: totalBeforeTax.Minor(),
					TotalAfterTax:  totalAfterTax.Minor(),
					OrderType:      l.OrderType,
				})

				for _, m := range l.Modifiers {
					modifierRows = append(modifierRows, data.SaleLineModifierRow{
						ID:              uuid.NewString(),
						SaleLineID:      lineID,
						GroupID:         m.GroupID,
						OptionID:        m.OptionID,
						GroupName:       m.GroupName,
						OptionName:      m.OptionName,
						PriceDeltaMinor: m.PriceDeltaMinor,
					})
				}

				if l.LineDiscount.IsPositive() {
					discountRows = append(discountRows, data.SaleDiscountRow{
						ID:     uuid.NewString(),
						SaleID: saleID,
						LineID: lineID,
						Type:   "fixed",
						Value:  l.LineDiscount.Minor(),
						Amount: l.LineDiscount.Minor(),
						Reason: "line_discount",
					})
				}

				// Stock movement: negative for sale, positive for return.
				qty := l.Qty
				if in.SaleType == "sale" {
					qty = -qty
				}
				movements = append(movements, data.StockMovementInput{
					ItemID:     l.ItemID,
					VariantID:  l.VariantID,
					LocationID: l.LocationID,
					SaleLineID: lineID,
					Type:       in.SaleType,
					Quantity:   qty,
					ActorID:    in.ActorID,
				})
			}

			// FK ordering: sale_line_modifiers.sale_line_id,
			// sale_discounts.line_id and stock_movements.sale_line_id all
			// reference sale_lines(id) — the lines batch MUST land first.
			if err := repo.InsertSaleLinesBatch(ctx, tx, lineRows); err != nil {
				return err
			}
			if err := repo.InsertSaleLineModifiersBatch(ctx, tx, modifierRows); err != nil {
				return err
			}
			if err := repo.InsertSaleDiscountsBatch(ctx, tx, discountRows); err != nil {
				return err
			}
			if _, err := repo.RecordStockMovementsBatch(ctx, tx, movements, currentQtys); err != nil {
				return err
			}

			// Voucher issues (ut-docs#1008): one vouchers row (the liability)
			// plus one 'issue' transaction row, inside the SAME transaction
			// as the sale — the sale_charges precedent (ADR-0062) for a
			// sale-level financial event that is not an article line.
			for _, v := range in.VoucherIssues {
				if err := repo.CreateVoucher(ctx, tx, data.Voucher{
					ID:                  v.VoucherID,
					HolderLabel:         v.HolderLabel,
					OriginalAmountMinor: v.Amount.Minor(),
					BalanceMinor:        v.Amount.Minor(),
					Currency:            in.Currency,
					IssuedSaleID:        saleID,
					CreatedAt:           now,
				}); err != nil {
					return err
				}
				if err := repo.RecordVoucherTransaction(ctx, tx, data.VoucherTransaction{
					ID:          uuid.NewString(),
					VoucherID:   v.VoucherID,
					SaleID:      saleID,
					Type:        "issue",
					AmountMinor: v.Amount.Minor(),
					CreatedAt:   now,
				}); err != nil {
					return err
				}
			}

			for _, p := range in.Payments {
				if err := validateMaskedPAN(p.MaskedPAN); err != nil {
					return err
				}
				cardPresent := data.CardPresentFields{
					MaskedPAN:  p.MaskedPAN,
					AuthCode:   p.AuthCode,
					TerminalID: p.TerminalID,
					TraceID:    p.TraceID,
				}
				if err := repo.InsertPayment(ctx, tx, uuid.NewString(), saleID, p.MethodID, p.Amount.Minor(), valueOrDefault(p.Currency, in.Currency), p.Reference, p.ChangeGiven.Minor(), p.TipAmount.Minor(), valueOrDefault(p.TipRecipient, TipRecipientEmployee), p.VoucherID, time.Now().UTC().Format(time.RFC3339), cardPresent); err != nil {
					return err
				}
				// Tracked voucher redemption (ut-docs#1008): debit the
				// voucher's balance (fail-closed on unknown/inactive/
				// insufficient — the whole sale rolls back) and record the
				// redemption event alongside the payments row. The goods'
				// own tax figures are untouched: only the payment method
				// differs from a cash sale. AllowVoucherOverdraft
				// (ut-docs#1053, journal replay only) forces the debit past
				// the balance check — unknown/inactive still roll back.
				if p.VoucherID != "" {
					if err := repo.DebitVoucherForRedemption(ctx, tx, p.VoucherID, p.Amount.Minor(), in.AllowVoucherOverdraft); err != nil {
						return err
					}
					if err := repo.RecordVoucherTransaction(ctx, tx, data.VoucherTransaction{
						ID:          uuid.NewString(),
						VoucherID:   p.VoucherID,
						SaleID:      saleID,
						Type:        "redemption",
						AmountMinor: p.Amount.Minor(),
						CreatedAt:   now,
					}); err != nil {
						return err
					}
				}
			}

			if in.SaleType == "return" && in.OriginalSaleID != "" {
				if err := repo.InsertSaleLink(ctx, tx, uuid.NewString(), saleID, in.OriginalSaleID, "return"); err != nil {
					return err
				}
			}

			pluginVersions, err := repo.ListActivePluginVersions(ctx, tx)
			if err != nil {
				return err
			}
			plugins := make(map[string]string, len(pluginVersions))
			for _, p := range pluginVersions {
				plugins[p.ID] = p.Version
			}

			if err := repo.InsertAudit(ctx, tx, in.ActorID, "sale", saleID, auditAction(in.SaleType), map[string]any{
				"subtotal":      subtotal,
				"taxTotal":      taxTotal,
				"serviceCharge": serviceCharge,
				"total":         total,
				"action":        auditAction(in.SaleType),
				"reason":        in.Note,
				"offline":       in.Offline,
				"tender":        tenderType,
				"sync":          syncStatus,
				"plugins":       plugins,
				"ts":            time.Now().UTC().Format(time.RFC3339),
			}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
				return err
			}

			return nil
		})
		if err == nil {
			return saleID, nil
		}
		if errors.Is(err, errReceiptConflictRetry) {
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("insert sale: unable to allocate receipt number")
}

// validateVoucherID applies the one shared identifier rule for every surface
// a voucher id enters through (issue input, a payment's redemption id — and
// mirroring GET /api/vouchers/{id}'s own bound): at most 64 characters, no
// control characters.
func validateVoucherID(id string) error {
	if len(id) > 64 {
		return errors.New("voucher id must be at most 64 characters")
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return errors.New("voucher id contains control characters")
		}
	}
	return nil
}

// UpdateSaleStatus updates sale.status and writes audit_log. Status expected: open|parked|voided|refunded.
//
// Voiding a sale that ISSUED vouchers cascades to them (ut-docs#1008 review,
// blocker F2): each still-untouched voucher (balance == original_amount) is
// voided in the same transaction, so a voided sale can never leave behind a
// live, spendable voucher the till has no record of selling. If any of the
// sale's vouchers has already been (partly) redeemed elsewhere, the whole
// void FAILS with data.ErrVoucherRedeemedCannotVoid — fail-closed: what an
// already-spent voucher means for its voided issuing sale is a human
// decision, not semantics this code invents.
func UpdateSaleStatus(ctx context.Context, sqlDB *sql.DB, saleID, status, actorID, reason string) error {
	if saleID == "" {
		return errors.New("saleID required")
	}
	if status == "" {
		return errors.New("status required")
	}
	switch status {
	case "open", "parked", "voided", "refunded", "completed":
	default:
		return fmt.Errorf("invalid status: %s", status)
	}
	repo := data.NewPOSRepo(sqlDB)
	err := db.WithTx(ctx, sqlDB, func(tx *sql.Tx) error {
		if err := repo.UpdateSaleStatus(ctx, tx, saleID, status); err != nil {
			return err
		}
		if status == "voided" {
			if err := repo.VoidVouchersIssuedInSale(ctx, tx, saleID); err != nil {
				return err
			}
		}
		if err := repo.InsertAudit(ctx, tx, actorID, "sale", saleID, status, map[string]any{
			"reason":   reason,
			"status":   status,
			"ts":       time.Now().UTC().Format(time.RFC3339),
			"subtotal": 0,
			"taxTotal": 0,
			"total":    0,
		}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
			return err
		}
		return nil
	})
	return err
}

func validateLine(l SaleLineInput) error {
	if l.ItemID == "" && l.VariantID == "" {
		return errors.New("line requires item_id or variant_id")
	}
	if l.ItemID != "" && l.VariantID != "" {
		return errors.New("line cannot have both item_id and variant_id")
	}
	if l.Qty <= 0 {
		return errors.New("quantity must be > 0")
	}
	if l.UnitPrice < 0 {
		return errors.New("unit price must be >= 0")
	}
	if l.LocationID == "" {
		return errors.New("location_id is required")
	}
	return nil
}

func generateReceiptNo() string {
	// numeric-ish receipt no derived from timestamp for readability
	n := time.Now().UnixNano() % 1000000000
	return fmt.Sprintf("%09d", n)
}

func valueOrDefault(val, def string) string {
	if val != "" {
		return val
	}
	return def
}

func auditAction(saleType string) string {
	if saleType == "return" {
		return "refund"
	}
	return "complete"
}

type PaymentFailure struct {
	SaleID   string
	ActorID  string
	Reason   string
	Payments []PaymentInput
	Lines    []SaleLineInput
	Total    int64
	Currency string
}

// RecordPaymentFailure logs a recoverable payment failure attempt for later retry/audit.
func RecordPaymentFailure(ctx context.Context, sqlDB *sql.DB, failure PaymentFailure) (string, error) {
	repo := data.NewPOSRepo(sqlDB)
	return repo.RecordPaymentFailure(ctx, data.PaymentFailure{
		SaleID:   failure.SaleID,
		ActorID:  failure.ActorID,
		Reason:   failure.Reason,
		Payments: toAnySlice(failure.Payments),
		Lines:    toAnySlice(failure.Lines),
		Total:    failure.Total,
		Currency: failure.Currency,
	})
}

func toAnySlice[T any](in []T) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
