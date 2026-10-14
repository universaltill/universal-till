package pages

import (
	"context"
	"errors"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// The kinds a scanned code can be (ut-docs#4344). /api/pos/scan's `kind`
// field names one of them when the cashier picked it on the chooser, or
// when the poster already knows (a tile is always its item).
const (
	scanKindItem     = "item"
	scanKindVoucher  = "voucher"
	scanKindCustomer = "customer"
	scanKindPromo    = "promo"
	scanKindReceipt  = "receipt"
)

func isScanKind(k string) bool {
	switch k {
	case scanKindItem, scanKindVoucher, scanKindCustomer, scanKindPromo, scanKindReceipt:
		return true
	}
	return false
}

// scanAnswers is every exact answer a code has. Item and voucher carry what
// was read, so acting on the answer doesn't resolve the item (the costliest
// part of a scan) or read the voucher / ask the main till a second time.
type scanAnswers struct {
	choices []pos.ScanChoice
	item    *pos.BasketLine
	voucher *data.Voucher
}

// resolveScanAnswers looks a code up as every kind (ut-docs#4344, owner
// direction on #4342): item by exact code, voucher, customer, promo and
// receipt. The old handler answered the first kind that matched, so a code
// that is legitimately two things became whichever came first, silently.
// The reads are local, read-only indexed point lookups: the voucher, promo
// and receipt checks share one query (POSRepo.ScanCodeKinds), so an
// item-only scan pays one extra round trip (BenchmarkScanItemOnly). The only network call is the main-till voucher
// fallback, and only for a GS- code with no local voucher -- the same place
// the handler already made it (ADR-0003).
//
// Customer lookups stay limited to looksLikeCustomerCode codes:
// LookupCustomer also matches a phone number, which would turn ordinary
// numeric barcodes into customers. Name search is never an answer here; the
// caller still falls back to it for typed entry when nothing matched.
func resolveScanAnswers(ctx context.Context, d *common.Deps, repo *data.POSRepo, locale, code string) scanAnswers {
	var out scanAnswers
	item, itemOK := d.Engine.ResolveExact(code)
	if itemOK {
		out.item = &item
	} else if d.Engine.HasScanCache(code) || d.Engine.HasLine(code) {
		// A line already in the basket (a resumed held sale, an item since
		// removed from the catalog) is still that item; the handler's own
		// fast path adds it, so there is no resolved line to hand over.
		item, itemOK = pos.BasketLine{Name: code}, true
	}
	// A read error leaves every kind unanswered: the scan then behaves as
	// before this lookup existed, never blocks (ADR-0003).
	isVoucher, promoOK, receipt, _ := repo.ScanCodeKinds(ctx, code, d.Engine.CustomerID())
	var v data.Voucher
	vErr := data.ErrVoucherNotFound
	if isVoucher {
		v, vErr = repo.GetVoucherBalance(ctx, nil, code)
	}
	var custName string
	var custOK bool
	if looksLikeCustomerCode(code) {
		_, custName, custOK = repo.LookupCustomer(ctx, code)
	}

	voucherOK := vErr == nil
	if !voucherOK && looksLikeVoucherCode(code) && errors.Is(vErr, data.ErrVoucherNotFound) {
		v, voucherOK = fetchVoucherFromPrimary(ctx, d, voucherProxyClient, code)
	}

	// Order is the chooser's row order: the most common intent first.
	if itemOK {
		detail := item.Name
		if !item.PriceCents.IsZero() {
			detail += " · " + httpx.FormatMoneyDisplay(item.PriceCents.Minor(), locale)
		}
		out.choices = append(out.choices, pos.ScanChoice{Kind: scanKindItem, Label: httpx.T(locale, "pos.scan_choice.item"), Detail: detail})
	}
	if voucherOK {
		out.voucher = &v
		balance := httpx.FormatMoneyDisplay(v.BalanceMinor, locale)
		if v.Currency != "" {
			balance = httpx.FormatMoneyIn(v.BalanceMinor, v.Currency, locale)
		}
		out.choices = append(out.choices, pos.ScanChoice{Kind: scanKindVoucher, Label: httpx.T(locale, "pos.scan_choice.voucher"), Detail: v.ID + " · " + balance})
	}
	if custOK {
		out.choices = append(out.choices, pos.ScanChoice{Kind: scanKindCustomer, Label: httpx.T(locale, "pos.scan_choice.customer"), Detail: custName})
	}
	if promoOK {
		out.choices = append(out.choices, pos.ScanChoice{Kind: scanKindPromo, Label: httpx.T(locale, "pos.scan_choice.promo"), Detail: code})
	}
	if receipt {
		out.choices = append(out.choices, pos.ScanChoice{Kind: scanKindReceipt, Label: httpx.T(locale, "pos.scan_choice.receipt"), Detail: code})
	}
	return out
}
