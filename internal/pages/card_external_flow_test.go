package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#6755: the shop's own card terminal (ADR-0205 §7).

func addExternal(t *testing.T, dpRepo *data.CardReaderRepo, name string) data.CardReader {
	t.Helper()
	c, err := dpRepo.Create(context.Background(), data.CardReader{Name: name, MethodKey: data.PaymentMethodCardExternal, Kind: data.CardReaderKindExternal})
	if err != nil {
		t.Fatalf("add external: %v", err)
	}
	return c
}

func postExtTenderForm(t *testing.T, mux *http.ServeMux, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func extForm(reader, auth, last4 string) url.Values {
	return url.Values{"method": {"card_external"}, "amount": {"120"}, "reader": {reader}, "auth_code": {auth}, "last4": {last4}}
}

func TestExternalTender_FormPaidStoresFieldsAndAuditsWithoutThem(t *testing.T) {
	mux, dp := readerRig(t)
	ext := addExternal(t, data.NewCardReaderRepo(dp.Db), "Counter")
	scan(t, dp)
	rec := postExtTenderForm(t, mux, extForm(ext.ID, "AB12CD", "4242"))
	if rec.Code != http.StatusOK {
		t.Fatalf("tender = %d %s", rec.Code, rec.Body.String())
	}
	var method, term, auth, pan string
	if err := dp.Db.QueryRow(`SELECT method_id, COALESCE(terminal_id,''), COALESCE(auth_code,''), COALESCE(masked_pan,'') FROM payments`).Scan(&method, &term, &auth, &pan); err != nil {
		t.Fatal(err)
	}
	if method != "card_external" || term != ext.ReaderRef || auth != "AB12CD" || pan != "•••• 4242" {
		t.Fatalf("payment = %q %q %q %q", method, term, auth, pan)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM audit_log WHERE action='external_card_tender'`); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
	var payload string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action='external_card_tender'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "4242") || strings.Contains(payload, "AB12CD") || !strings.Contains(payload, `"has_auth_code":true`) {
		t.Fatalf("audit payload = %s", payload)
	}
}

func TestExternalTender_EmptyFieldsStillCompletes(t *testing.T) {
	mux, dp := readerRig(t)
	ext := addExternal(t, data.NewCardReaderRepo(dp.Db), "Counter")
	scan(t, dp)
	if rec := postExtTenderForm(t, mux, extForm(ext.ID, "", "")); rec.Code != http.StatusOK {
		t.Fatalf("tender = %d %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM payments WHERE method_id='card_external' AND COALESCE(auth_code,'')='' AND COALESCE(masked_pan,'')=''`); n != 1 {
		t.Fatalf("payment row not as expected: %d", n)
	}
	var payload string
	_ = dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action='external_card_tender'`).Scan(&payload)
	if !strings.Contains(payload, `"has_auth_code":false`) || !strings.Contains(payload, `"has_last4":false`) {
		t.Fatalf("audit payload = %s", payload)
	}
}

func TestExternalTender_InvalidFieldsAre422AndRecordNothing(t *testing.T) {
	mux, dp := readerRig(t)
	ext := addExternal(t, data.NewCardReaderRepo(dp.Db), "Counter")
	scan(t, dp)
	for name, form := range map[string]url.Values{"last4": extForm(ext.ID, "", "42x2"), "auth": extForm(ext.ID, "AB-12", "")} {
		rec := postExtTenderForm(t, mux, form)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		if code, _ := errCode(t, rec.Body.String()); code != "card_reader.external.invalid" {
			t.Errorf("%s: code %q", name, code)
		}
	}
	if countRows(t, dp, `SELECT COUNT(*) FROM sales`) != 0 || countRows(t, dp, `SELECT COUNT(*) FROM payments`) != 0 {
		t.Fatal("invalid fields must record nothing")
	}
	if len(dp.Engine.Basket().Lines) != 1 {
		t.Fatal("basket must be untouched")
	}
}

func TestExternalTender_WithoutExternalRowIs409(t *testing.T) {
	mux, dp := readerRig(t)
	scan(t, dp)
	rec := postExtTenderForm(t, mux, extForm("", "", ""))
	if rec.Code != http.StatusConflict {
		t.Fatalf("no row: %d %s", rec.Code, rec.Body.String())
	}
	if code, _ := errCode(t, rec.Body.String()); code != "reader_required" {
		t.Errorf("code %q", code)
	}
	if countRows(t, dp, `SELECT COUNT(*) FROM payments`) != 0 {
		t.Fatal("nothing may be recorded")
	}
}

func TestExternalTender_FieldsIgnoredForOtherMethods(t *testing.T) {
	mux, dp := readerRig(t)
	scan(t, dp)
	form := url.Values{"method": {"cash"}, "amount": {"120"}, "auth_code": {"AB12CD"}, "last4": {"4242"}}
	if rec := postExtTenderForm(t, mux, form); rec.Code != http.StatusOK {
		t.Fatalf("cash = %d %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM payments WHERE COALESCE(auth_code,'')='' AND COALESCE(masked_pan,'')=''`); n != 1 {
		t.Fatalf("stray fields stored: %d", n)
	}
}

func TestExternalTender_SplitJSONCashPlusExternal(t *testing.T) {
	mux, dp := readerRig(t)
	ext := addExternal(t, data.NewCardReaderRepo(dp.Db), "Counter")
	scan(t, dp)
	rec := postTender(t, mux, tenderBody(leg("cash", 50, ""), leg("card_external", 70, ext.ID)))
	if rec.Code != http.StatusOK {
		t.Fatalf("split = %d %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM payments WHERE method_id IN ('cash','card_external')`); n != 2 {
		t.Fatalf("legs = %d", n)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM payments WHERE method_id='card_external' AND terminal_id='`+ext.ReaderRef+`'`); n != 1 {
		t.Fatal("external leg must carry the row ref")
	}
}

func TestCardReaderGrid_ExternalIsNeverItsOwnButton(t *testing.T) {
	_, dp := readerRig(t)
	ctx := context.Background()
	grid := []data.PaymentMethod{{ID: "cash"}, {ID: "card"}, {ID: "card_external"}}
	got, v := cardReaderGrid(ctx, dp, grid, "")
	if len(got) != 2 || v.Show {
		t.Fatalf("no row: grid %v show %v", got, v.Show)
	}
	addExternal(t, data.NewCardReaderRepo(dp.Db), "Counter")
	got, v = cardReaderGrid(ctx, dp, grid, "")
	if !v.Show || !v.LegacyCard {
		t.Fatalf("with row: show %v legacy %v", v.Show, v.LegacyCard)
	}
	for _, m := range got {
		if m.ID == "card_external" {
			t.Fatal("card_external must not be a grid button")
		}
	}
}

func TestExternalSettings_AddNeedsManagerAndNoPlugin(t *testing.T) {
	mux, dp := authRig(t)
	form := url.Values{"kind": {"external"}, "name": {"My terminal"}}
	if rec := postReaderForm(t, mux, "/settings/card-readers", "cashier", form); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d", rec.Code)
	}
	if rec := postReaderForm(t, mux, "/settings/card-readers", "manager", form); rec.Code != http.StatusOK {
		t.Fatalf("manager = %d %s", rec.Code, rec.Body.String())
	}
	rows, _ := data.NewCardReaderRepo(dp.Db).ListActive(context.Background())
	if len(rows) != 1 || rows[0].Kind != data.CardReaderKindExternal || rows[0].PluginID != "" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestExternalPicker_ListsReadyRowWithoutPlugin(t *testing.T) {
	mux, dp := pickerRig(t)
	addExternal(t, data.NewCardReaderRepo(dp.Db), "Counter")
	body := getPickerHTML(t, mux, "?source=pos")
	for _, want := range []string{`data-kind="external"`, "Works without internet", `data-state="ready"`, `id="card-reader-external"`} {
		if !strings.Contains(body, want) {
			t.Errorf("picker lacks %q", want)
		}
	}
}

func TestPaymentMethodKey_CardExternal(t *testing.T) {
	if k, ok := httpx.PaymentMethodKey("card_external"); !ok || k != "tender.card_external" {
		t.Fatalf("got %q %v", k, ok)
	}
}
