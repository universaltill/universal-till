package pages

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#7064 slice 1: refund rows name and link their original sale; the
// original carries a Refunded / Partly refunded / Refund pending chip.

func refundJournalGet(t *testing.T, mux *http.ServeMux, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d (%s)", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func seedRefundLinkPage(t *testing.T, d *common.Deps, returnID, originalID string) {
	t.Helper()
	if _, err := d.Db.Exec(`INSERT INTO sale_links(id, sale_id, original_sale_id, reason) VALUES (?,?,?, 'refund')`, "lnk-"+returnID, returnID, originalID); err != nil {
		t.Fatalf("seed link: %v", err)
	}
}

// rowFor returns the <tr> of the journal row for a receipt.
func rowFor(t *testing.T, body, receipt string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<tr class="journal-row"[^>]*data-receipt="` + regexp.QuoteMeta(receipt) + `".*?</tr>`).FindString(body)
	if m == "" {
		t.Fatalf("no journal row for %s in: %s", receipt, body)
	}
	return m
}

func TestJournalUI_RefundRowLinksOriginalAndSaleShowsRefundedChip(t *testing.T) {
	mux, d := newJournalMux(t)
	seedJournalPageSale(t, d, "sale-1", "R-100", "sale")
	seedJournalPageSale(t, d, "sale-2", "R-101", "return")
	seedRefundLinkPage(t, d, "sale-2", "sale-1")
	seedJournalPageSale(t, d, "sale-3", "R-102", "sale") // plain sale

	body := refundJournalGet(t, mux, "/ui/journal?limit=full")

	ret := rowFor(t, body, "R-101")
	if !strings.Contains(ret, `href="/journal/R-100"`) || !strings.Contains(ret, "Refund of") {
		t.Fatalf("return row must link its original: %s", ret)
	}
	if !strings.Contains(ret, "-£3.00") {
		t.Fatalf("return row must show a negative amount: %s", ret)
	}
	// UX: no status chip on a return row.
	if strings.Contains(ret, "data-refund-status") || strings.Contains(ret, "chip-note") {
		t.Fatalf("return row must carry no status chip: %s", ret)
	}

	orig := rowFor(t, body, "R-100")
	if !strings.Contains(orig, `data-refund-status="refunded"`) || !strings.Contains(orig, "Refunded") || !strings.Contains(orig, `aria-hidden="true"`) {
		t.Fatalf("original row must carry the Refunded chip: %s", orig)
	}

	plain := rowFor(t, body, "R-102")
	if strings.Contains(plain, "data-refund-status") || strings.Contains(plain, "journal-refund") {
		t.Fatalf("plain sale must have no chip/margin block: %s", plain)
	}
}

func TestJournalUI_PartlyRefundedShowsAmounts(t *testing.T) {
	mux, d := newJournalMux(t)
	seedJournalPageSale(t, d, "sale-1", "R-100", "sale")
	seedJournalPageSale(t, d, "sale-2", "R-101", "return")
	if _, err := d.Db.Exec(`UPDATE sales SET total = 100 WHERE id = 'sale-2'`); err != nil {
		t.Fatal(err)
	}
	seedRefundLinkPage(t, d, "sale-2", "sale-1")
	orig := rowFor(t, refundJournalGet(t, mux, "/ui/journal?limit=full"), "R-100")
	if !strings.Contains(orig, `data-refund-status="partly"`) || !strings.Contains(orig, "Partly refunded") || !strings.Contains(orig, "£1.00 of £3.00 refunded") {
		t.Fatalf("want partly chip with amounts: %s", orig)
	}
	if strings.Contains(orig, "data-refund-status=\"refunded\"") {
		t.Fatalf("partly must not read refunded: %s", orig)
	}
}

func TestJournalUI_PendingRefundChipBeatsRefunded(t *testing.T) {
	mux, d := newJournalMux(t)
	seedJournalPageSale(t, d, "sale-1", "R-100", "sale")
	seedJournalPageSale(t, d, "sale-2", "R-101", "return")
	seedRefundLinkPage(t, d, "sale-2", "sale-1")
	for _, s := range []string{"asked", "unknown"} {
		if _, err := d.Db.Exec(`INSERT INTO payment_leg_journal(request_id, ask, state, method_id, amount_minor, currency, tip_minor, reader_ref, transaction_ref, sale_id, reason, at)
			VALUES ('refund:sale-1:stripe:q1', 1, ?, 'stripe', 300, 'GBP', 0, '', '', '', '', '2026-01-02T10:00:00.000000000Z')`, s); err != nil {
			t.Fatalf("seed leg: %v", err)
		}
	}
	orig := rowFor(t, refundJournalGet(t, mux, "/ui/journal?limit=full"), "R-100")
	if !strings.Contains(orig, `data-refund-status="pending"`) || !strings.Contains(orig, "Refund pending") || !strings.Contains(orig, "chip-warn") {
		t.Fatalf("want pending warn chip: %s", orig)
	}
	if strings.Contains(orig, ">Refunded<") || strings.Contains(orig, "Refunded</span>") {
		t.Fatalf("pending must never read Refunded: %s", orig)
	}
	if strings.Contains(orig, "of £3.00 refunded") {
		t.Fatalf("pending must not show the refunded amount: %s", orig)
	}
}

func TestJournalUI_StatusLookupFailureDegradesToNoChips(t *testing.T) {
	mux, d := newJournalMux(t)
	seedJournalPageSale(t, d, "sale-1", "R-100", "sale")
	seedJournalPageSale(t, d, "sale-2", "R-101", "return")
	seedRefundLinkPage(t, d, "sale-2", "sale-1")
	if _, err := d.Db.Exec(`DROP TABLE payment_leg_journal`); err != nil {
		t.Fatal(err)
	}
	body := refundJournalGet(t, mux, "/ui/journal?limit=full")
	for _, r := range []string{"R-100", "R-101"} {
		row := rowFor(t, body, r)
		if strings.Contains(row, "data-refund-status") || strings.Contains(row, "Refund of") {
			t.Fatalf("degraded row %s must have no chips/links: %s", r, row)
		}
	}
}

func TestJournalDetail_RefundChipAmountAndTypeLabels(t *testing.T) {
	mux, d := newJournalMux(t)
	seedJournalPageSale(t, d, "sale-1", "R-100", "sale")
	seedJournalPageSale(t, d, "sale-2", "R-101", "return")
	seedRefundLinkPage(t, d, "sale-2", "sale-1")

	orig := refundJournalGet(t, mux, "/journal/R-100")
	if !strings.Contains(orig, `data-refund-status="refunded"`) || !strings.Contains(orig, "£3.00 of £3.00 refunded") {
		t.Fatalf("detail must show chip and amounts: %s", orig)
	}
	if !strings.Contains(orig, `<span class="tag">Sale</span>`) || strings.Contains(orig, `<span class="tag">sale</span>`) {
		t.Fatalf("detail type tag must be translated Sale: %s", orig)
	}
	ret := refundJournalGet(t, mux, "/journal/R-101")
	if !strings.Contains(ret, `<span class="tag">Refund</span>`) || strings.Contains(ret, `<span class="tag">return</span>`) {
		t.Fatalf("detail type tag must be translated Refund: %s", ret)
	}
	if strings.Contains(ret, "-£3.00") || !strings.Contains(ret, "£3.00") {
		t.Fatalf("return detail total must be positive (negative only in the list): %s", ret)
	}
	if strings.Contains(ret, "data-refund-status") {
		t.Fatalf("return detail has no status chip: %s", ret)
	}
}

func TestJournalDetail_PendingExplainsDoNotRefundAgain(t *testing.T) {
	mux, d := newJournalMux(t)
	seedJournalPageSale(t, d, "sale-1", "R-100", "sale")
	seedJournalPageSale(t, d, "sale-2", "R-101", "return")
	seedRefundLinkPage(t, d, "sale-2", "sale-1")
	for _, s := range []string{"asked", "unknown"} {
		if _, err := d.Db.Exec(`INSERT INTO payment_leg_journal(request_id, ask, state, method_id, amount_minor, currency, tip_minor, reader_ref, transaction_ref, sale_id, reason, at)
			VALUES ('refund:sale-1:stripe:q1', 1, ?, 'stripe', 300, 'GBP', 0, '', '', '', '', '2026-01-02T10:00:00.000000000Z')`, s); err != nil {
			t.Fatal(err)
		}
	}
	body := refundJournalGet(t, mux, "/journal/R-100")
	if !strings.Contains(body, "Refund pending") || !strings.Contains(body, "Do not start a new refund") || !strings.Contains(body, "tap Refund again") || !strings.Contains(body, "Payments needing attention") {
		t.Fatalf("pending detail must explain: %s", body)
	}
	if strings.Contains(body, "of £3.00 refunded") {
		t.Fatalf("pending detail must not show the refunded amount: %s", body)
	}
}
