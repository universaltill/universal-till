package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#3340: age-restricted sales at the cashier till. A basket line for
// an items.age_restricted item needs an ACCEPTED ID check recorded this sale
// (POST /api/pos/age-check) before /api/pos/tender may complete; the
// age_verifications row is written in the sale's own transaction with the
// real sale id.

// newAgeCheckPOSDeps is newPOSTestDeps plus one age-restricted item
// (itm-cider / CIDER, flagged in the DB) and one legacy line whose
// resolver output LACKS the flag (OLDCIDER -> the same restricted item),
// proving the tender gate's DB backstop.
func newAgeCheckPOSDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newPOSTestDeps(t)
	for _, s := range []string{
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active,age_restricted) VALUES('itm-cider','CIDER','Cider',300,'tax_std',1,1)`,
		`INSERT INTO inventory(id,item_id,variant_id,location_id,quantity,updated_at) VALUES('inv-cider','itm-cider',NULL,'loc_main',50,datetime('now'))`,
	} {
		if _, err := dp.Db.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	resolver := stubResolver{
		"ABC":      {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", TaxRateBP: 2000},
		"CIDER":    {SKU: "CIDER", Name: "Cider", Qty: 1, PriceCents: 300, ItemID: "itm-cider", TaxRateBP: 2000, AgeRestricted: true},
		"OLDCIDER": {SKU: "OLDCIDER", Name: "Cider", Qty: 1, PriceCents: 300, ItemID: "itm-cider", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)
	engine.SetChargePolicyAsker(&pluginChargePolicyAsker{db: dp.Db})
	dp.Engine = engine
	return mux, dp
}

func cashTender(mux *http.ServeMux) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader("method=cash"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func countRows(t *testing.T, dp *common.Deps, q string) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func lineKeyBySKU(t *testing.T, dp *common.Deps, sku string) string {
	t.Helper()
	for _, l := range dp.Engine.Basket().Lines {
		if l.SKU == sku {
			return l.LineKey
		}
	}
	t.Fatalf("no basket line for %s", sku)
	return ""
}

// The persistent per-line badge: visible on the basket as soon as the
// restricted item is rung up, before anyone tries to tender.
func TestBasket_RestrictedLineShowsNeedsIDCheckBadge(t *testing.T) {
	mux, dp := newAgeCheckPOSDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Engine.Scan("CIDER"); err != nil {
		t.Fatal(err)
	}
	key := lineKeyBySKU(t, dp, "CIDER")
	rec := posPostForm(mux, "/api/pos/line", "key="+key+"&qty=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("line update: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Count(body, `class="age-check-toggle`) != 1 {
		t.Fatalf("exactly the one restricted line must carry an ID-check toggle:\n%s", body)
	}
	for _, want := range []string{"Needs ID check", `aria-haspopup="dialog"`, `aria-expanded="false"`, `aria-label="Needs ID check — Record the ID check for this item"`, "age-check-sheet", ">Accept<", ">Refuse<"} {
		if !strings.Contains(body, want) {
			t.Fatalf("basket missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "showModal") {
		t.Fatal("the ID-check sheet must never use showModal()")
	}
	// ut-docs#3340 review: the help topic must be reachable from the
	// feature itself, inside the ID-check sheet — not only from the
	// catalog's "Age restricted" checkbox.
	sheet := body[strings.Index(body, `<dialog class="modifier-modal age-check-sheet"`):]
	sheet = sheet[:strings.Index(sheet, `</dialog>`)]
	if !strings.Contains(sheet, `href="/help/age-restricted-sales"`) {
		t.Fatalf("the ID-check sheet must link the age-restricted-sales help topic:\n%s", sheet)
	}
}

func TestTender_BlocksUnverifiedRestrictedLine(t *testing.T) {
	mux, dp := newAgeCheckPOSDeps(t)
	if _, err := dp.Engine.Scan("CIDER"); err != nil {
		t.Fatal(err)
	}
	key := lineKeyBySKU(t, dp, "CIDER")

	rec := cashTender(mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("blocked tender re-renders the basket in place (200), got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `Record an ID check for &#34;Cider&#34; before taking payment.`) {
		t.Fatalf("blocked tender must explain what to do next:\n%s", body)
	}
	// The blocked line's sheet opens itself (elevation_prompt.html's
	// self-opening .show() pattern), not just a flat error.
	if !strings.Contains(body, `data-age-check-autoopen`) || !strings.Contains(body, key) {
		t.Fatalf("blocked tender must auto-open the unresolved line's sheet:\n%s", body)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sales`); n != 0 {
		t.Fatalf("a blocked tender must create no sale row, got %d", n)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM age_verifications`); n != 0 {
		t.Fatalf("no verification row without a sale, got %d", n)
	}
	if len(dp.Engine.Basket().Lines) != 1 {
		t.Fatal("the basket must survive a blocked tender")
	}
}

func TestTender_BlocksRefusedRestrictedLine(t *testing.T) {
	mux, dp := newAgeCheckPOSDeps(t)
	if _, err := dp.Engine.Scan("CIDER"); err != nil {
		t.Fatal(err)
	}
	key := lineKeyBySKU(t, dp, "CIDER")
	rec := posPostForm(mux, "/api/pos/age-check", "key="+key+"&outcome=refused")
	if rec.Code != http.StatusOK {
		t.Fatalf("record refusal: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Refuse says what to do next, in the sheet that stays open.
	if !strings.Contains(body, "Remove it from the basket to continue") || !strings.Contains(body, "data-age-check-autoopen") {
		t.Fatalf("a refusal must keep the sheet open with the remove-the-item guidance:\n%s", body)
	}
	if !strings.Contains(body, "ID refused") {
		t.Fatalf("the line badge must show the refused state:\n%s", body)
	}

	rec = cashTender(mux)
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sales`); n != 0 {
		t.Fatalf("a refused restricted line must block tender, got %d sales (%s)", n, rec.Body.String())
	}
}

func TestTender_AcceptedCheckCompletesSaleAndLogsVerificationInSameSale(t *testing.T) {
	mux, dp := newAgeCheckPOSDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Engine.Scan("CIDER"); err != nil {
		t.Fatal(err)
	}
	key := lineKeyBySKU(t, dp, "CIDER")
	// First refused, then the customer produced ID: the final answer wins.
	posPostForm(mux, "/api/pos/age-check", "key="+key+"&outcome=refused")
	rec := posPostForm(mux, "/api/pos/age-check", "key="+key+"&outcome=accepted")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ID checked") {
		t.Fatalf("record acceptance: %d %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM age_verifications`); n != 0 {
		t.Fatalf("recording a check must not write a row before the sale exists, got %d", n)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(`{"payments":[{"method":"cash","amount":480}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Data struct {
			SaleID string `json:"saleId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.Data.SaleID == "" {
		t.Fatalf("tender response: %v %s", err, rr.Body.String())
	}
	var saleID, itemID, itemName, outcome, cashier string
	if err := dp.Db.QueryRow(`SELECT sale_id, item_id, item_name, outcome, cashier_id FROM age_verifications`).
		Scan(&saleID, &itemID, &itemName, &outcome, &cashier); err != nil {
		t.Fatalf("expected exactly one verification row: %v", err)
	}
	if saleID != out.Data.SaleID || itemID != "itm-cider" || itemName != "Cider" || outcome != "accepted" || cashier == "" {
		t.Fatalf("verification row: sale=%s (want %s) item=%s name=%s outcome=%s cashier=%q", saleID, out.Data.SaleID, itemID, itemName, outcome, cashier)
	}
	if len(dp.Engine.AgeChecks()) != 0 {
		t.Fatal("a completed sale must clear the ID checks for the next customer")
	}
}

// A refused item the cashier then removed: the refusal is still logged
// with the sale that went ahead without it.
func TestTender_RefusedThenRemovedItemIsLoggedWithSale(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newAgeCheckPOSDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Engine.Scan("CIDER"); err != nil {
		t.Fatal(err)
	}
	key := lineKeyBySKU(t, dp, "CIDER")
	posPostForm(mux, "/api/pos/age-check", "key="+key+"&outcome=refused")
	if rec := posPostForm(mux, "/api/pos/remove", "key="+key+"&reason=void"); rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	cashTender(mux)
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sales`); n != 1 {
		t.Fatalf("with the refused item removed the sale goes ahead, got %d sales", n)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM age_verifications WHERE outcome = 'refused' AND sale_id = (SELECT id FROM sales)`); n != 1 {
		t.Fatalf("the refusal must be logged with the sale, got %d", n)
	}
}

// Backstop: a line whose resolver output lacks the flag (a held sale saved
// before the item was flagged) is still caught from the DB at tender time.
func TestTender_DBBackstopCatchesUnflaggedRestrictedLine(t *testing.T) {
	mux, dp := newAgeCheckPOSDeps(t)
	if _, err := dp.Engine.Scan("OLDCIDER"); err != nil {
		t.Fatal(err)
	}
	rec := cashTender(mux)
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sales`); n != 0 {
		t.Fatalf("the DB says itm-cider is restricted: tender must block, got %d sales", n)
	}
	if !strings.Contains(rec.Body.String(), "age-check-sheet") {
		t.Fatalf("the backstop must surface the ID-check sheet for the line it caught:\n%s", rec.Body.String())
	}
}

func TestAgeCheckHandler_RejectsBadInput(t *testing.T) {
	mux, dp := newAgeCheckPOSDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Engine.Scan("CIDER"); err != nil {
		t.Fatal(err)
	}
	cider := lineKeyBySKU(t, dp, "CIDER")
	apple := lineKeyBySKU(t, dp, "ABC")
	for name, form := range map[string]string{
		"bad outcome":       "key=" + cider + "&outcome=maybe",
		"missing key":       "outcome=accepted",
		"unknown key":       "key=nope&outcome=accepted",
		"unrestricted line": "key=" + apple + "&outcome=accepted",
	} {
		if rec := posPostForm(mux, "/api/pos/age-check", form); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", name, rec.Code)
		}
	}
	if len(dp.Engine.AgeChecks()) != 0 {
		t.Fatalf("rejected requests must record nothing: %+v", dp.Engine.AgeChecks())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/pos/age-check", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: want 405, got %d", rec.Code)
	}
}
