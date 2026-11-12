package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#1043: GET /ui/shifts/{id}/count -- a closed shift's stored
// denomination count, read back. Instants derive from the clock.

func (f *xpFx) closedShift(t *testing.T, id string, counted int64, protocol any) {
	t.Helper()
	f.exec(t, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,closed_at,opening_cash,closing_cash,expected_cash,count_protocol) VALUES(?,?,?,?,?,?,?,?,?)`,
		id, "reg1", "u1", xpAt(f.t0, -5*time.Hour), xpAt(f.t0, -4*time.Hour), 500, counted, counted, protocol)
}

func (f *xpFx) getCount(t *testing.T, id string, hdr map[string]string, user *auth.User) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/ui/shifts/"+id+"/count", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func mustContainAll(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
}

func TestShiftCountDetail_GBPLinesTotalAndMismatch(t *testing.T) {
	f := xpFixture(t)
	f.closedShift(t, "c1", 10310, `{"10":1,"5000":2,"100":3}`)
	rec := f.getCount(t, "c1", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	mustContainAll(t, body, "£50.00", "£100.00", "£1.00", "£3.00", "£0.10", "£103.10", "Total of the count", "Counted", "empty:shift-count-body focus:shift-count-title")
	if strings.Index(body, "£50.00") > strings.Index(body, "£1.00") {
		t.Errorf("highest denomination must come first:\n%s", body)
	}
	if strings.Contains(body, "differs from this piece count") || strings.Contains(body, "could not be read") {
		t.Errorf("no mismatch/partial note expected when totals agree:\n%s", body)
	}

	f.closedShift(t, "c2", 9999, `{"5000":2,"100":3,"10":1}`)
	body = f.getCount(t, "c2", nil, nil).Body.String()
	mustContainAll(t, body, "£99.99", "differs from this piece count")
	if strings.Contains(body, "class='error'") || strings.Contains(body, `class="error"`) || strings.Contains(body, "stock-low") {
		t.Errorf("mismatch must be a neutral note:\n%s", body)
	}
}

func TestShiftCountDetail_CurrencyFollowsShop(t *testing.T) {
	f := xpFixture(t)
	f.closedShift(t, "c1", 2000, `{"1000":2}`)
	pinCurrency(t, "JPY")
	body := f.getCount(t, "c1", nil, nil).Body.String()
	mustContainAll(t, body, "¥1,000", "¥2,000")
	if strings.Contains(body, "¥1,000.") || strings.Contains(body, "¥2,000.") {
		t.Errorf("0-decimal currency must show no decimals:\n%s", body)
	}
	pinCurrency(t, "EUR")
	mustContainAll(t, f.getCount(t, "c1", nil, nil).Body.String(), "€")
}

func TestShiftCountDetail_NotRecorded(t *testing.T) {
	f := xpFixture(t)
	f.closedShift(t, "n1", 700, nil)
	f.closedShift(t, "n2", 700, "{}")
	for _, id := range []string{"n1", "n2"} {
		rec := f.getCount(t, id, nil, nil)
		body := rec.Body.String()
		if rec.Code != 200 {
			t.Fatalf("%s: code %d", id, rec.Code)
		}
		mustContainAll(t, body, "No denomination count was recorded for this shift.")
		if strings.Contains(body, "<table") || strings.Contains(body, "class='error'") || strings.Contains(body, `class="error"`) {
			t.Errorf("%s: no table/error expected:\n%s", id, body)
		}
	}
}

func TestShiftCountDetail_DamagedAndHostile(t *testing.T) {
	f := xpFixture(t)
	f.closedShift(t, "p1", 200, `{"abc":3,"200":1}`)
	rec := f.getCount(t, "p1", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("partial: %d", rec.Code)
	}
	mustContainAll(t, rec.Body.String(), "£2.00", "Part of this count could not be read")

	// Partial read whose readable total differs from Counted: the dropped
	// entries are unknown, so no mismatch note (review of #1043).
	f.closedShift(t, "p2", 500, `{"abc":3,"200":1}`)
	body := f.getCount(t, "p2", nil, nil).Body.String()
	mustContainAll(t, body, "Part of this count could not be read")
	if strings.Contains(body, "differs from this piece count") {
		t.Errorf("partial read must not claim a mismatch:\n%s", body)
	}

	f.closedShift(t, "u1x", 0, `{"100":`)
	rec = f.getCount(t, "u1x", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("corrupt: %d", rec.Code)
	}
	body = rec.Body.String()
	mustContainAll(t, body, "This count could not be read.")
	if strings.Contains(body, "<table") || strings.Contains(body, "differs from this piece count") {
		t.Errorf("unreadable: no table or mismatch note:\n%s", body)
	}

	f.closedShift(t, "x1", 0, `{"<script>alert(1)</script>":1}`)
	body = f.getCount(t, "x1", nil, nil).Body.String()
	if strings.Contains(body, "<script") || strings.Contains(body, "alert(1)") {
		t.Errorf("stored text must never reach the page:\n%s", body)
	}
}

func TestShiftCountDetail_Errors(t *testing.T) {
	f := xpFixture(t) // sh1 is open
	cases := []struct {
		id   string
		code int
		text string
		json string
	}{
		{"sh1", 404, "not closed yet or no longer exists", "shift_not_closed"},
		{"nope", 404, "not closed yet or no longer exists", "shift_not_closed"},
		{strings.Repeat("a", 129), 400, "request could not be read", "invalid_request"},
	}
	for _, c := range cases {
		rec := f.getCount(t, c.id, nil, nil)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.text) || !strings.Contains(rec.Body.String(), "role='alert'") {
			t.Errorf("%.8s html: %d %q", c.id, rec.Code, rec.Body.String())
		}
		rec = f.getCount(t, c.id, jsonHdr, nil)
		var env struct {
			Data  any `json:"data"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != c.code || env.Error.Code != c.json || env.Data != nil {
			t.Errorf("%.8s json: %d %q err=%v", c.id, rec.Code, rec.Body.String(), err)
		}
	}
}

func TestShiftCountDetail_JSONShape(t *testing.T) {
	f := xpFixture(t)
	f.closedShift(t, "j1", 10310, `{"5000":2,"100":3,"10":1}`)
	f.closedShift(t, "j2", 700, nil)

	rec := f.getCount(t, "j1", jsonHdr, nil)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || rec.Code != 200 || string(raw["error"]) != "null" {
		t.Fatalf("envelope: %d %s", rec.Code, rec.Body.String())
	}
	var d map[string]any
	if err := json.Unmarshal(raw["data"], &d); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"shift_id", "opened_at", "closed_at", "currency", "recorded", "readable", "partial", "entries", "total_minor", "counted_minor"} {
		if _, ok := d[k]; !ok {
			t.Errorf("data lacks %q: %s", k, raw["data"])
		}
	}
	if d["shift_id"] != "j1" || d["currency"] != "GBP" || d["recorded"] != true || d["readable"] != true || d["partial"] != false ||
		d["total_minor"] != float64(10310) || d["counted_minor"] != float64(10310) {
		t.Errorf("data = %s", raw["data"])
	}
	entries := d["entries"].([]any)
	first := entries[0].(map[string]any)
	if len(entries) != 3 || first["denomination_minor"] != float64(5000) || first["count"] != float64(2) || first["line_total_minor"] != float64(10000) {
		t.Errorf("entries = %v", entries)
	}

	rec = f.getCount(t, "j2", jsonHdr, nil)
	if !strings.Contains(rec.Body.String(), `"entries":[]`) || !strings.Contains(rec.Body.String(), `"recorded":false`) {
		t.Errorf("empty case: %s", rec.Body.String())
	}
}

// TestShiftCountDetail_CashierSeesItWithAuthOn calls the mux without the auth
// middleware: it proves the handler itself adds no role or PIN check for a
// cashier (design D1). Session enforcement for /ui/ is auth's own concern.
func TestShiftCountDetail_CashierSeesItWithAuthOn(t *testing.T) {
	f := xpFixture(t)
	f.closedShift(t, "a1", 10000, `{"5000":2}`)
	t.Setenv("UT_AUTH", "on")
	rec := f.getCount(t, "a1", nil, &auth.User{ID: "u1", Role: "cashier"})
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, body)
	}
	mustContainAll(t, body, "£50.00", "£100.00")
	if strings.Contains(body, "override_pin") {
		t.Errorf("no PIN prompt expected (design D1):\n%s", body)
	}
}

func TestShiftCountDetail_ReadOnly(t *testing.T) {
	f := xpFixture(t)
	f.closedShift(t, "r1", 10000, `{"5000":2}`)
	snap := func() string {
		var out []string
		for _, q := range []string{
			`SELECT COUNT(*) || ':' || COALESCE(group_concat(id || closed_at || closing_cash || COALESCE(count_protocol,''), '|'), '') FROM shifts`,
			`SELECT COUNT(*) FROM audit_log`,
			`SELECT COUNT(*) FROM cloud_outbox`,
		} {
			var s string
			if err := f.dp.Db.QueryRow(q).Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return strings.Join(out, "#")
	}
	before := snap()
	if rec := f.getCount(t, "r1", nil, nil); rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	f.getCount(t, "r1", jsonHdr, nil)
	f.getCount(t, "sh1", nil, nil)
	if after := snap(); after != before {
		t.Errorf("route wrote something:\nbefore %s\nafter  %s", before, after)
	}
}

func TestShiftsPage_HistoryRowsOfferShowCountOnlyWhenClosed(t *testing.T) {
	f := xpFixture(t) // sh1 open
	f.closedShift(t, "c1", 10000, `{"5000":2}`)
	body := f.get(t)
	mustContainAll(t, body, `hx-get="/ui/shifts/c1/count"`, `id="shift-count-body"`, `id="shift-count-title"`, "Show count", "Tap Show count on a closed shift")
	if strings.Contains(body, "/ui/shifts/sh1/count") {
		t.Errorf("open shift must have no Show count button")
	}
	if strings.Contains(body, `id="shift-count" class="x-report" aria-live`) || strings.Contains(body, `aria-live="polite" id="shift-count"`) {
		t.Errorf("no aria-live on #shift-count (UX A5)")
	}
	// history header: 6 data columns plus the empty actions column.
	if n := strings.Count(historyHead(body), "<th>"); n != 7 {
		t.Errorf("history header cells = %d, want 7", n)
	}
}

func TestShiftsPage_NoClosedHistoryHidesCountSection(t *testing.T) {
	f := xpFixture(t) // only an open shift
	body := f.get(t)
	if strings.Contains(body, `id="shift-count"`) {
		t.Errorf("section must render only when history has a closed row")
	}
}

// historyHead returns the <thead> of the History table (the one after the
// "History" card heading).
func historyHead(body string) string {
	i := strings.Index(body, "<h2>History</h2>")
	if i < 0 {
		return ""
	}
	rest := body[i:]
	s := strings.Index(rest, "<thead")
	e := strings.Index(rest, "</thead>")
	if s < 0 || e < 0 {
		return ""
	}
	return rest[s:e]
}
