package pages

import (
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// The refund page's bound manager approval (ut-docs#7134, same mechanism as
// #7085/#7096): the PIN only counts for the exact refund the prompt showed.

// refundApprovalFields are the prompt's hidden fields, canonical and in a
// fixed order: everything the retry posts that decides what is refunded.
func refundApprovalFields(detail data.SaleDetail, method string, form url.Values) []elevationHiddenField {
	offline := "0"
	if formFlagTruthy(form.Get("offline")) {
		offline = "1"
	}
	f := []elevationHiddenField{
		{Name: "receipt", Value: detail.ReceiptNo},
		{Name: "method", Value: method}, // already lower/trim/default-cash
		{Name: "refund_leg", Value: strings.TrimSpace(form.Get("refund_leg"))},
		{Name: "offline", Value: offline},
		{Name: "refund_attempt_id", Value: strings.TrimSpace(form.Get("refund_attempt_id"))},
	}
	for i := range detail.Lines { // index order = numeric N order
		n := "qty_" + strconv.Itoa(i)
		f = append(f, elevationHiddenField{Name: n, Value: refundQtyCanon(form.Get(n))})
	}
	return f
}

// refundQtyCanon: trimmed; "" is "0"; a number is written without trailing
// zeros ("1.50" -> "1.5"); anything unparsable stays raw (unreachable, the
// line parser refuses it first).
func refundQtyCanon(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "0"
	}
	q, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	return strconv.FormatFloat(q, 'f', -1, 64)
}

// refundApprovalExtras are the server-side facts the form does not carry: the
// sale, the refund total, what was already returned per line, and what each
// payment still has left / is about to get back. Map keys are sorted so the
// same state always yields the same binding.
func refundApprovalExtras(detail data.SaleDetail, guard refundGuardState, total money.Money,
	legs []refundLeg, legRefunds []data.RefundLegRefund) []string {
	out := []string{"sale", detail.ID, "currency", detail.Currency, "total", strconv.FormatInt(total.Minor(), 10), "returned"}
	keys := make([]string, 0, len(guard.returnedQtyByLine))
	for k := range guard.returnedQtyByLine {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k, strconv.FormatFloat(guard.returnedQtyByLine[k], 'f', -1, 64))
	}
	out = append(out, "legs")
	for _, l := range legs {
		out = append(out, l.PaymentID, strconv.FormatInt(l.Remaining, 10))
	}
	out = append(out, "alloc")
	for _, a := range legRefunds {
		out = append(out, a.OriginalPaymentID, strconv.FormatInt(a.Amount, 10))
	}
	return out
}
