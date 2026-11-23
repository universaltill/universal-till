package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/cardreaders"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6566: picker endpoint and manager routes.

type pickerJSON struct {
	Data struct {
		Method     string `json:"method"`
		Default    string `json:"default"`
		Pinned     bool   `json:"pinned"`
		Auto       bool   `json:"auto"`
		Selectable int    `json:"selectable"`
		Readers    []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Method     string `json:"method"`
			Kind       string `json:"kind"`
			State      string `json:"state"`
			Reason     string `json:"reason"`
			Selectable bool   `json:"selectable"`
		} `json:"readers"`
	} `json:"data"`
}

func getPicker(t *testing.T, mux *http.ServeMux, query string) (*httptest.ResponseRecorder, pickerJSON) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/pos/card-readers"+query, nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out pickerJSON
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func stateAnswer(states map[string]string) func(refs []string) (json.RawMessage, error) {
	return func(refs []string) (json.RawMessage, error) {
		var items []string
		for _, r := range refs {
			st := states[r]
			if st == "" {
				st = "ready"
			}
			items = append(items, `{"reader_id":"`+r+`","state":"`+st+`"}`)
		}
		return json.RawMessage(`{"readers":[` + strings.Join(items, ",") + `]}`), nil
	}
}

func pickerRig(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := readerRig(t)
	registerCardReaders(mux, dp)
	return mux, dp
}

func TestPicker_StatesDefaultAndLastUsed(t *testing.T) {
	mux, dp := pickerRig(t)
	sumup := seedReaderPlugin(t, dp, "p.sumup", "sumup", true)
	sumup.state = stateAnswer(map[string]string{"rdr_2": "offline", "rdr_3": "busy"})
	a := addReader(t, dp, sumup, "Counter 1", "rdr_1")
	addReader(t, dp, sumup, "Counter 2", "rdr_2")
	addReader(t, dp, sumup, "Counter 3", "rdr_3")
	c4 := addReader(t, dp, sumup, "Counter 4", "rdr_4")

	rec, got := getPicker(t, mux, "?method=sumup&source=pos")
	if rec.Code != http.StatusOK || len(got.Data.Readers) != 4 {
		t.Fatalf("picker: %d %s", rec.Code, rec.Body.String())
	}
	byName := map[string]struct {
		State, Reason string
		Sel           bool
	}{}
	for _, r := range got.Data.Readers {
		byName[r.Name] = struct {
			State, Reason string
			Sel           bool
		}{r.State, r.Reason, r.Selectable}
	}
	if x := byName["Counter 2"]; x.State != "offline" || x.Sel || x.Reason != "tap.reason.reader_unreachable" {
		t.Errorf("offline row: %+v", x)
	}
	if x := byName["Counter 3"]; x.State != "busy" || x.Sel || x.Reason != "tap.reason.reader_busy" {
		t.Errorf("busy row: %+v", x)
	}
	if x := byName["Counter 1"]; x.State != "ready" || !x.Sel || x.Reason != "" {
		t.Errorf("ready row: %+v", x)
	}
	if got.Data.Default != a.ID || got.Data.Auto || got.Data.Selectable != 2 {
		t.Fatalf("default=%s (want first selectable %s) auto=%v selectable=%d", got.Data.Default, a.ID, got.Data.Auto, got.Data.Selectable)
	}
	// last used on this till becomes the default, and survives a repo reopen.
	if err := data.NewCardReaderRepo(dp.Db).TouchLastUsed(context.Background(), c4.ID); err != nil {
		t.Fatal(err)
	}
	cardreaders.SharedCache().Reset()
	if _, got = getPicker(t, mux, "?method=sumup"); got.Data.Default != c4.ID {
		t.Fatalf("default must be the last used reader, got %s", got.Data.Default)
	}
	// the last-used reader is removed: back to the first ready one.
	if _, err := data.NewCardReaderRepo(dp.Db).SoftRemove(context.Background(), c4.ID); err != nil {
		t.Fatal(err)
	}
	cardreaders.SharedCache().Reset()
	if _, got = getPicker(t, mux, "?method=sumup"); got.Data.Default != a.ID {
		t.Fatalf("removed default must fall back to the first ready reader, got %s", got.Data.Default)
	}
}

// Tester finding (#6566): the default row of a till that never took a card
// payment on any reader was tagged "Last used", which is false.
func TestPicker_LastUsedTagOnlyWhenAReaderWasUsed(t *testing.T) {
	mux, dp := pickerRig(t)
	sumup := seedReaderPlugin(t, dp, "p.sumup", "sumup", true)
	a := addReader(t, dp, sumup, "Counter 1", "rdr_1")
	addReader(t, dp, sumup, "Counter 2", "rdr_2")
	html := func() string {
		cardreaders.SharedCache().Reset()
		req := httptest.NewRequest(http.MethodGet, "/api/pos/card-readers?method=sumup&source=pos", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("picker html: %d", rec.Code)
		}
		return rec.Body.String()
	}
	if strings.Contains(html(), "card-reader-default-tag") {
		t.Fatal("no reader was ever used: the default row must not say 'Last used'")
	}
	if err := data.NewCardReaderRepo(dp.Db).TouchLastUsed(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	if strings.Count(html(), "card-reader-default-tag") != 1 {
		t.Fatal("after a payment the last used reader carries the tag exactly once")
	}
}

func TestPicker_OneReadyReaderMeansNoPicker(t *testing.T) {
	mux, dp := pickerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	rp.state = stateAnswer(map[string]string{"tmr_B": "offline"})
	a := addReader(t, dp, rp, "A", "tmr_A")
	addReader(t, dp, rp, "B", "tmr_B")
	_, got := getPicker(t, mux, "?method=demopay")
	if !got.Data.Auto || got.Data.Default != a.ID {
		t.Fatalf("exactly one ready reader: auto submit on it, got %+v", got.Data)
	}
}

func TestPicker_NoneReadyAndPluginFailureIsUnknownButSelectable(t *testing.T) {
	mux, dp := pickerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	rp.state = stateAnswer(map[string]string{"tmr_A": "offline", "tmr_B": "needs_setup"})
	addReader(t, dp, rp, "A", "tmr_A")
	addReader(t, dp, rp, "B", "tmr_B")
	_, got := getPicker(t, mux, "?method=demopay")
	if got.Data.Selectable != 0 || got.Data.Default != "" || got.Data.Auto {
		t.Fatalf("none ready: %+v", got.Data)
	}
	// the plugin errors out (a timeout, non-zero exit, offline): unknown, still selectable.
	cardreaders.SharedCache().Reset()
	rp.mu.Lock()
	rp.state = nil
	rp.mu.Unlock()
	_, got = getPicker(t, mux, "?method=demopay")
	for _, r := range got.Data.Readers {
		if r.State != "unknown" || !r.Selectable || r.Reason != "card_reader.reason.unknown" {
			t.Fatalf("a failed check is unknown and selectable: %+v", r)
		}
	}
}

func TestPicker_PluginStateIsCachedBriefly(t *testing.T) {
	mux, dp := pickerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	rp.state = stateAnswer(nil)
	addReader(t, dp, rp, "A", "tmr_A")
	addReader(t, dp, rp, "B", "tmr_B")
	getPicker(t, mux, "?method=demopay")
	getPicker(t, mux, "?method=demopay")
	rp.mu.Lock()
	n := rp.stateReq
	rp.mu.Unlock()
	if n != 1 {
		t.Fatalf("the provider must be asked once within the cache TTL, asked %d times", n)
	}
}

func TestPicker_CoreBusyBeatsProvider(t *testing.T) {
	mux, dp := pickerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	rp.state = stateAnswer(nil)
	a := addReader(t, dp, rp, "A", "tmr_A")
	addReader(t, dp, rp, "B", "tmr_B")
	cardreaders.SharedInFlight().Acquire(a.ID)
	defer cardreaders.SharedInFlight().Release(a.ID)
	_, got := getPicker(t, mux, "?method=demopay")
	for _, r := range got.Data.Readers {
		if r.ID == a.ID && (r.State != "busy" || r.Selectable) {
			t.Fatalf("a reader in use on this till is busy whatever the provider says: %+v", r)
		}
	}
}

func TestPicker_LockedTenderIsPinnedToItsReader(t *testing.T) {
	mux, dp := pickerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	rp.state = stateAnswer(nil)
	a := addReader(t, dp, rp, "A", "tmr_A")
	addReader(t, dp, rp, "B", "tmr_B")
	rp.answer = func(int) string { return "UNKNOWN" }
	scan(t, dp)
	postTender(t, mux, tenderBody(leg("demopay", 120, a.ID)))
	_, got := getPicker(t, mux, "?method=demopay")
	if !got.Data.Pinned || len(got.Data.Readers) != 1 || got.Data.Readers[0].ID != a.ID || !got.Data.Readers[0].Selectable {
		t.Fatalf("a locked tender pins its reader: %+v", got.Data)
	}
}

func TestPicker_KioskSourceAndBadMethod(t *testing.T) {
	mux, _ := pickerRig(t)
	if rec, _ := getPicker(t, mux, "?source=kiosk"); rec.Code != http.StatusForbidden {
		t.Fatalf("the self-order kiosk has no picker: %d", rec.Code)
	}
	if rec, _ := getPicker(t, mux, "?method=../x"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad method: %d", rec.Code)
	}
}

func TestPicker_ListsOnlyManagedMethodsAndHidesTapToPayWithoutBridge(t *testing.T) {
	mux, dp := pickerRig(t)
	managed := seedReaderPlugin(t, dp, "p.sumup", "sumup", true)
	legacy := seedReaderPlugin(t, dp, "p.old", "oldpay", false)
	managed.state = stateAnswer(nil)
	addReader(t, dp, managed, "Counter", "rdr_1")
	addReader(t, dp, legacy, "Old", "rdr_old")
	if _, err := data.NewCardReaderRepo(dp.Db).Create(context.Background(), data.CardReader{Name: "Card (tap)", MethodKey: "sumup", PluginID: "p.sumup", ReaderRef: "tap_1", Kind: data.CardReaderKindTapToPay, Source: data.CardReaderSourceDevice}); err != nil {
		t.Fatal(err)
	}
	_, got := getPicker(t, mux, "")
	if len(got.Data.Readers) != 1 || got.Data.Readers[0].Name != "Counter" {
		t.Fatalf("only managed, non tap_to_pay (no device bridge) readers: %+v", got.Data.Readers)
	}
	// with a device bridge registered the Tap to Pay row is listed, in the same picker ...
	cardreaders.TapToPayAvailable = func() bool { return true }
	defer func() { cardreaders.TapToPayAvailable = func() bool { return false } }()
	cardreaders.SharedCache().Reset()
	if _, got = getPicker(t, mux, ""); len(got.Data.Readers) != 2 {
		t.Fatalf("tap_to_pay row must be listed once a bridge exists: %+v", got.Data.Readers)
	}
	// ... and the self-order kiosk never gets it, bridge or not.
	var cErr *cardReaderError
	if _, err := resolveReaderLegs(context.Background(), dp, []pos.PaymentInput{{MethodID: "sumup", ReaderRowID: findReaderID(t, dp, "tap_1")}}, "kiosk"); !errors.As(err, &cErr) || cErr.Code != readerCodeInvalid {
		t.Fatalf("a kiosk tender on a tap_to_pay row must be refused, got %v", err)
	}
}

func findReaderID(t *testing.T, dp *common.Deps, ref string) string {
	t.Helper()
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM card_readers WHERE reader_ref = ?`, ref).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// ---- manager routes ----

func authRig(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := pickerRig(t)
	t.Setenv("UT_AUTH", "")
	dp.AuthSvc = auth.NewService(dp.Db)
	return mux, dp
}

func asRole(req *http.Request, role string) *http.Request {
	return auth.WithUser(req, auth.User{ID: role + "-1", Role: role})
}

func postReaderForm(t *testing.T, mux *http.ServeMux, path, role string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, asRole(req, role))
	return rec
}

func TestCardReaderRoutes_CashierSeesPickerButEveryManagerRouteIs403(t *testing.T) {
	mux, dp := authRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	rp.state = stateAnswer(nil)
	a := addReader(t, dp, rp, "A", "tmr_A")
	addReader(t, dp, rp, "B", "tmr_B")

	req := httptest.NewRequest(http.MethodGet, "/api/pos/card-readers?method=demopay", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, asRole(req, "cashier"))
	if rec.Code != http.StatusOK {
		t.Fatalf("cashier picker = %d", rec.Code)
	}

	get := httptest.NewRequest(http.MethodGet, "/settings/card-readers", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, asRole(get, "cashier"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /settings/card-readers as cashier = %d, want 403", rec.Code)
	}
	for name, tc := range map[string]struct {
		path string
		form url.Values
	}{
		"add":    {"/settings/card-readers", url.Values{"name": {"X"}, "method_key": {"demopay"}, "reader_ref": {"tmr_X"}}},
		"edit":   {"/settings/card-readers/" + a.ID, url.Values{"name": {"Z"}}},
		"remove": {"/settings/card-readers/" + a.ID + "/remove", url.Values{}},
	} {
		if rec := postReaderForm(t, mux, tc.path, "cashier", tc.form); rec.Code != http.StatusForbidden {
			t.Errorf("%s as cashier = %d, want 403", name, rec.Code)
		}
	}
	rows, _ := data.NewCardReaderRepo(dp.Db).ListActive(context.Background())
	if len(rows) != 2 || rows[0].Name != "A" {
		t.Fatalf("a refused cashier request must change nothing: %+v", rows)
	}
}

func TestCardReaderRoutes_ManagerAddRenameMoveUnattendedRemove(t *testing.T) {
	mux, dp := authRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	old := seedReaderPlugin(t, dp, "p.old", "oldpay", false)
	_ = old
	repo := data.NewCardReaderRepo(dp.Db)
	ctx := context.Background()
	if _, err := dp.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('manager-1','manager1','Manager One','x','manager')`); err != nil {
		t.Fatal(err)
	}

	// add: valid
	rec := postReaderForm(t, mux, "/settings/card-readers", "manager", url.Values{"name": {"Front"}, "method_key": {"demopay"}, "reader_ref": {"tmr_F"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", rec.Code, rec.Body.String())
	}
	rows, _ := repo.ListActive(ctx)
	if len(rows) != 1 || rows[0].PluginID != "p.demo" || rows[0].Source != data.CardReaderSourceManual {
		t.Fatalf("add: %+v", rows)
	}
	// add: a plugin without the hook is refused, a bad ref and a duplicate name too
	for name, f := range map[string]url.Values{
		"no hook":   {"name": {"Old"}, "method_key": {"oldpay"}, "reader_ref": {"x1"}},
		"bad ref":   {"name": {"Bad"}, "method_key": {"demopay"}, "reader_ref": {"a/b"}},
		"dup name":  {"name": {"front"}, "method_key": {"demopay"}, "reader_ref": {"tmr_2"}},
		"no method": {"name": {"Nope"}, "method_key": {"ghost"}, "reader_ref": {"tmr_3"}},
	} {
		if rec := postReaderForm(t, mux, "/settings/card-readers", "manager", f); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: want 422, got %d", name, rec.Code)
		}
	}
	if rows, _ = repo.ListActive(ctx); len(rows) != 1 {
		t.Fatalf("refused adds must add nothing: %d rows", len(rows))
	}
	id := rows[0].ID
	if rec := postReaderForm(t, mux, "/settings/card-readers/"+id, "manager", url.Values{"name": {"Counter"}}); rec.Code != http.StatusOK {
		t.Fatalf("rename = %d", rec.Code)
	}
	if rec := postReaderForm(t, mux, "/settings/card-readers/"+id, "manager", url.Values{"unattended": {"1"}}); rec.Code != http.StatusOK {
		t.Fatalf("unattended = %d", rec.Code)
	}
	got, _ := repo.Get(ctx, id)
	if got.Name != "Counter" || !got.UnattendedDefault {
		t.Fatalf("edit did not stick: %+v", got)
	}
	if rec := postReaderForm(t, mux, "/settings/card-readers/"+id+"/remove", "manager", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d", rec.Code)
	}
	if rows, _ = repo.ListActive(ctx); len(rows) != 0 {
		t.Fatalf("remove: %+v", rows)
	}
	if rec := postReaderForm(t, mux, "/settings/card-readers/nope/remove", "manager", url.Values{}); rec.Code != http.StatusNotFound {
		t.Fatalf("remove of an unknown id = %d, want 404", rec.Code)
	}
	var audits int
	_ = dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'card_reader'`).Scan(&audits)
	if audits < 4 {
		t.Fatalf("every manager change is audited, got %d rows", audits)
	}
	_ = rp
}

func TestCardReaderRoutes_ManagerPageRenders(t *testing.T) {
	mux, dp := authRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	addReader(t, dp, rp, "Front counter", "tmr_F")
	req := httptest.NewRequest(http.MethodGet, "/settings/card-readers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, asRole(req, "manager"))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("manager page = %d: %s", rec.Code, body)
	}
	for _, want := range []string{`data-testid="card-reader-row"`, "Front counter", `id="card-reader-add-form"`, `id="card-reader-ref"`, `id="card-reader-provider"`, `id="card-reader-count"`} {
		if !strings.Contains(body, want) {
			t.Errorf("manager page misses %s", want)
		}
	}
}

func TestCardReaderRoutes_RemovingTheLegacyEqualReaderClearsTheSetting(t *testing.T) {
	mux, dp := authRig(t)
	seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	if _, err := dp.Db.Exec(`INSERT INTO plugin_settings(id,plugin_id,key,value_json,scope) VALUES('s1','p.demo','demopay_reader_id','"tmr_L"','register')`); err != nil {
		t.Fatal(err)
	}
	if err := dp.Pm.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ := data.NewCardReaderRepo(dp.Db).ListActive(context.Background())
	if len(rows) != 1 {
		t.Fatalf("migrated rows: %d", len(rows))
	}
	if rec := postReaderForm(t, mux, "/settings/card-readers/"+rows[0].ID+"/remove", "manager", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d", rec.Code)
	}
	var n int
	_ = dp.Db.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id='p.demo' AND key='demopay_reader_id'`).Scan(&n)
	if n != 0 {
		t.Fatalf("the legacy setting must be cleared so the till does not fall back to the removed reader")
	}
}

func TestCardReaderGrid_OneCardButtonReplacesManagedMethodsOnly(t *testing.T) {
	_, dp := pickerRig(t)
	ctx := context.Background()
	grid := []data.PaymentMethod{{ID: "cash", Name: "tender.cash"}, {ID: "sumup", Name: "SumUp"}, {ID: "stripe", Name: "Stripe"}, {ID: "oldpay", Name: "Old"}}
	// no registry: nothing changes (legacy tills see no difference).
	if got, v := cardReaderGrid(ctx, dp, grid, ""); len(got) != 4 || v.Show {
		t.Fatalf("no readers: grid must be untouched: %+v %+v", got, v)
	}
	sumup := seedReaderPlugin(t, dp, "p.sumup", "sumup", true)
	stripe := seedReaderPlugin(t, dp, "p.stripe", "stripe", true)
	old := seedReaderPlugin(t, dp, "p.old", "oldpay", false)
	addReader(t, dp, old, "Old", "o1") // a row on a plugin without the hook stays a plain button
	if got, v := cardReaderGrid(ctx, dp, grid, ""); len(got) != 4 || v.Show {
		t.Fatalf("a method that cannot honour reader_id keeps its button: %+v %+v", got, v)
	}
	a := addReader(t, dp, sumup, "Counter", "rdr_1")
	addReader(t, dp, stripe, "Tablet", "tmr_1")
	if err := data.NewCardReaderRepo(dp.Db).TouchLastUsed(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	got, v := cardReaderGrid(ctx, dp, grid, "")
	ids := []string{}
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "cash,oldpay" || !v.Show || v.First || v.DefaultName != "Counter" {
		t.Fatalf("grid=%v view=%+v", ids, v)
	}
	if _, v = cardReaderGrid(ctx, dp, grid, "sumup"); !v.First {
		t.Fatalf("the shop's preferred method is a reader method: the Card button leads")
	}
	// O1: no generic "card" button next to it, so the reader button reads plain "Card".
	if v.LegacyCard {
		t.Fatalf("no generic card method in the grid: LegacyCard must be false: %+v", v)
	}
	// O1: a till that still has the generic "card" method gets two Card buttons;
	// the view says so, so the template can label the reader one "Card reader".
	withCard := append([]data.PaymentMethod{}, grid...)
	withCard = append(withCard, data.PaymentMethod{ID: "card", Name: "tender.card"})
	if _, v = cardReaderGrid(ctx, dp, withCard, ""); !v.Show || !v.LegacyCard {
		t.Fatalf("generic card + registry readers: LegacyCard must be set: %+v", v)
	}
}

func getPickerHTML(t *testing.T, mux *http.ServeMux, query string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/pos/card-readers"+query, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("picker html = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestPickerFragment_MarkupContract(t *testing.T) {
	mux, dp := pickerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	rp.state = stateAnswer(map[string]string{"tmr_B": "offline"})
	a := addReader(t, dp, rp, "Front counter", "tmr_A")
	b := addReader(t, dp, rp, "Back office", "tmr_B")
	addReader(t, dp, rp, "Kitchen", "tmr_C")
	body := getPickerHTML(t, mux, "?source=pos")
	for _, want := range []string{
		`id="card-reader-picker"`, `data-testid="card-reader-picker"`, `role="radiogroup"`, `id="card-reader-list"`,
		`id="card-reader-live"`, `aria-live="polite"`, `data-testid="card-reader-confirm"`, `data-testid="card-reader-back"`,
		`data-reader-id="` + a.ID + `"`, `data-reader-id="` + b.ID + `"`, `role="radio"`, `Which reader?`,
		`name="reader" value="` + a.ID + `"`, // the default row is pre-selected
		`aria-checked="true"`, `aria-describedby="card-reader-reason-` + b.ID + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("picker misses %s", want)
		}
	}
	// the offline row is aria-disabled (not `disabled`), with its visible reason
	i := strings.Index(body, `data-reader-id="`+b.ID+`"`)
	row := body[i : i+strings.Index(body[i:], "</button>")]
	if !strings.Contains(row, `aria-disabled="true"`) || strings.Contains(row, ` disabled`) || !strings.Contains(row, "Reader not reachable.") || !strings.Contains(row, "Offline") {
		t.Errorf("offline row: %s", row)
	}
	if strings.Contains(body, `data-testid="card-reader-pinned"`) || strings.Contains(body, `data-testid="card-reader-none-ready"`) {
		t.Errorf("neither pinned nor none-ready here")
	}
	if strings.Contains(body, "tmr_A") || strings.Contains(body, "tmr_B") {
		t.Errorf("the browser must only ever see registry row ids, never a provider reader id")
	}
}

func TestPickerFragment_NoneReadyAndAuto(t *testing.T) {
	mux, dp := pickerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	rp.state = stateAnswer(map[string]string{"tmr_A": "offline", "tmr_B": "busy"})
	addReader(t, dp, rp, "A", "tmr_A")
	addReader(t, dp, rp, "B", "tmr_B")
	body := getPickerHTML(t, mux, "?method=demopay")
	if !strings.Contains(body, `data-testid="card-reader-none-ready"`) || !strings.Contains(body, `<button type="submit" id="card-reader-confirm"`) || !strings.Contains(body, " disabled") {
		t.Errorf("none ready: reason line, Back, and Confirm disabled")
	}
	cardreaders.SharedCache().Reset()
	rp.mu.Lock()
	rp.state = stateAnswer(map[string]string{"tmr_B": "offline"})
	rp.mu.Unlock()
	body = getPickerHTML(t, mux, "?method=demopay")
	if !strings.Contains(body, `data-auto="1"`) {
		t.Errorf("exactly one ready reader: the panel auto-submits")
	}
}

// The whole chain on a REAL compiled wasm guest (internal/plugins/testdata/
// readers_guest) through the real runtime: `.readers` must be dispatched
// BLOCKING (WasmRuntime.Sync marks it), the answer parsed into states, and an
// authorize on the chosen reader carries reader_id and lands in terminal_id.
func TestPicker_RealWasmGuestStatesAndTender(t *testing.T) {
	mux, dp := pickerRig(t)
	const pluginID = "com.test.wasm-readers"
	for _, q := range []string{
		`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at)
		 VALUES ('com.test.wasm-readers', '1.0.0', 'Wasm Readers', 'd', 'wasm', './plugin.wasm', 'url', 'sha', 'a', 's', '[]', '0.0.0', '1', datetime('now'))`,
		`INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level) VALUES ('com.test.wasm-readers', 'Wasm Readers', '1.0.0', 'installed', './plugin.wasm', 'wasm', 1, 'trusted')`,
		`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active) VALUES ('ew', 'com.test.wasm-readers', 'wasmcard', 'Wasm card', 'payment', 'payment.wasmcard.requested', 1)`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('hw1', 'com.test.wasm-readers', 'payment.wasmcard.authorize', 'a', 1), ('hw2', 'com.test.wasm-readers', 'payment.wasmcard.refund', 'a', 1), ('hw3', 'com.test.wasm-readers', 'payment.wasmcard.readers', 'a', 1)`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES ('pw', 'com.test.wasm-readers', 'events:receive', 1)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	out := filepath.Join(t.TempDir(), "readers.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./internal/plugins/testdata/readers_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build readers guest: %v\n%s", err, raw)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(paths.Plugins(), pluginID, "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	dp.Pm.Wasm.Sync(context.Background(), dp.Db)

	repo := data.NewCardReaderRepo(dp.Db)
	mk := func(name, ref string) data.CardReader {
		c, err := repo.Create(context.Background(), data.CardReader{Name: name, MethodKey: "wasmcard", PluginID: pluginID, ReaderRef: ref})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	front := mk("Front", "tmr_front")
	mk("Bar", "tmr_offline_bar")
	mk("Back", "tmr_busy_back")

	_, got := getPicker(t, mux, "?method=wasmcard")
	states := map[string]string{}
	for _, r := range got.Data.Readers {
		states[r.Name] = r.State
	}
	if states["Front"] != "ready" || states["Bar"] != "offline" || states["Back"] != "busy" {
		t.Fatalf("states from the real guest: %+v", states)
	}
	if got.Data.Auto != true || got.Data.Default != front.ID {
		t.Fatalf("one ready reader: auto %v default %s", got.Data.Auto, got.Data.Default)
	}

	scan(t, dp)
	rec := postTender(t, mux, tenderBody(leg("wasmcard", 120, front.ID)))
	if rec.Code != http.StatusOK || countRows(t, dp, "SELECT COUNT(*) FROM sales") != 1 {
		t.Fatalf("tender through the real guest: %d %s", rec.Code, rec.Body.String())
	}
	var term string
	if err := dp.Db.QueryRow(`SELECT terminal_id FROM payments`).Scan(&term); err != nil || term != "tmr_front" {
		t.Fatalf("terminal_id = %q err %v", term, err)
	}
}

// Reviewer (ut-docs#6566): a reader a payment is running on, or one a locked
// (outcome unknown) tender is pinned to, cannot be removed: the retry on that
// same reader is the only way the till learns whether the card was charged.
func TestCardReaderRoutes_RemoveIsRefusedWhileAPaymentNeedsTheReader(t *testing.T) {
	mux, dp := authRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	a := addReader(t, dp, rp, "A", "tmr_A")
	b := addReader(t, dp, rp, "B", "tmr_B")
	if _, err := dp.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('manager-1','manager1','Manager One','x','manager')`); err != nil {
		t.Fatal(err)
	}
	active := func() int {
		rows, _ := data.NewCardReaderRepo(dp.Db).ListActive(context.Background())
		return len(rows)
	}

	// in flight
	if !cardreaders.SharedInFlight().Acquire(a.ID) {
		t.Fatal("acquire")
	}
	rec := postReaderForm(t, mux, "/settings/card-readers/"+a.ID+"/remove", "manager", url.Values{})
	cardreaders.SharedInFlight().Release(a.ID)
	if rec.Code != http.StatusConflict || active() != 2 {
		t.Fatalf("remove of an in-flight reader = %d (active %d), want 409 and nothing removed", rec.Code, active())
	}

	// pinned by a locked tender
	dp.Engine.LockTender("sig", pos.PendingTender{Unknown: []pos.UnknownLeg{{MethodID: "demopay", RequestID: "r1", ReaderRowID: b.ID}}})
	rec = postReaderForm(t, mux, "/settings/card-readers/"+b.ID+"/remove", "manager", url.Values{})
	if rec.Code != http.StatusConflict || active() != 2 {
		t.Fatalf("remove of a pinned reader = %d (active %d), want 409 and nothing removed", rec.Code, active())
	}
	if !strings.Contains(rec.Body.String(), httpx.T("en", "card_reader.settings.err_in_use")) {
		t.Fatalf("the refusal must say why: %s", rec.Body.String())
	}
	dp.Engine.SettleTender()

	// free again: removable
	if rec := postReaderForm(t, mux, "/settings/card-readers/"+b.ID+"/remove", "manager", url.Values{}); rec.Code != http.StatusOK || active() != 1 {
		t.Fatalf("remove of a free reader = %d (active %d)", rec.Code, active())
	}
}

// ---- ut-docs#6756: hidden kinds and the offline note ----

func TestReaderPicker_HidesIneligibleKinds(t *testing.T) {
	mux, dp := pickerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	rp.state = stateAnswer(nil)
	repo := data.NewCardReaderRepo(dp.Db)
	ctx := context.Background()
	addReader(t, dp, rp, "Cloud", "r_cloud")
	mk := func(name, ref, kind, tr string) {
		if _, err := repo.Create(ctx, data.CardReader{Name: name, MethodKey: rp.Method, PluginID: rp.PluginID, ReaderRef: ref, Kind: kind, Transport: tr}); err != nil {
			t.Fatal(err)
		}
	}
	mk("Phone", "r_phone", data.CardReaderKindPhone, "")
	mk("Sdk", "r_sdk", data.CardReaderKindSDK, data.CardReaderTransportBluetooth)
	mk("Own", "r_ext", data.CardReaderKindExternal, "")
	rawReader(t, dp, rp, "Weird", "r_weird", "weird", "internet")
	rec, got := getPicker(t, mux, "")
	if rec.Code != http.StatusOK || len(got.Data.Readers) != 2 {
		t.Fatalf("only cloud and phone rows: %d %+v", rec.Code, got.Data.Readers)
	}
	for _, r := range got.Data.Readers {
		if r.Kind != "cloud" && r.Kind != "phone" {
			t.Errorf("unexpected kind %q", r.Kind)
		}
	}
	for _, key := range []string{`"transport"`, `"device_serial"`, `"bound_at"`} {
		if strings.Contains(rec.Body.String(), key) {
			t.Errorf("picker JSON must not carry %s", key)
		}
	}
}

func TestReaderPicker_ShowsOfflineNote(t *testing.T) {
	mux, dp := pickerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	rp.state = stateAnswer(nil)
	addReader(t, dp, rp, "Cloud", "r_cloud")
	if _, err := data.NewCardReaderRepo(dp.Db).Create(context.Background(), data.CardReader{Name: "Phone", MethodKey: rp.Method, PluginID: rp.PluginID, ReaderRef: "r_phone", Kind: data.CardReaderKindPhone}); err != nil {
		t.Fatal(err)
	}
	body := getPickerHTML(t, mux, "?source=pos")
	if n := strings.Count(body, `data-testid="card-reader-offline"`); n != 2 {
		t.Fatalf("one offline note per row, got %d", n)
	}
	if n := strings.Count(body, ">Needs internet</span>"); n != 2 {
		t.Fatalf("each row says Needs internet, got %d", n)
	}
}

func TestReaderOfflineKey_AllKinds(t *testing.T) {
	en := map[string]string{}
	raw, err := os.ReadFile("web/locales/en.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &en); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cloud", "sdk", "tap_to_pay", "phone", "weird", ""} {
		if got := readerOfflineKey(k); got != "card_reader.offline.needs_internet" {
			t.Errorf("%q -> %q", k, got)
		}
	}
	if got := readerOfflineKey("external"); got != "card_reader.offline.external" {
		t.Errorf("external -> %q", got)
	}
	for _, key := range []string{"card_reader.offline.needs_internet", "card_reader.offline.external", "card_reader.kind.sdk", "card_reader.kind.external"} {
		if en[key] == "" {
			t.Errorf("en.json misses %s", key)
		}
	}
	if _, ok := en["card_reader.kind."+"blue"+"tooth"]; ok {
		t.Errorf("the retired kind's label must be gone")
	}
}

// Review of #6756: SetUnattendedDefault refuses sdk/external readers, so the
// Settings list must not offer the kiosk-default checkbox on them — except on
// a row already ticked (a migrated bluetooth row), which must stay clearable.
func TestCardReaderRoutes_UnattendedCheckboxOnlyWhereItCanSucceed(t *testing.T) {
	mux, dp := authRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	addReader(t, dp, rp, "Front counter", "tmr_C")
	addReader(t, dp, rp, "Old BT", "tmr_S")
	addReader(t, dp, rp, "Old BT ticked", "tmr_T")
	if _, err := dp.Db.Exec(`UPDATE card_readers SET kind='sdk', transport='bluetooth' WHERE reader_ref IN ('tmr_S','tmr_T')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`UPDATE card_readers SET unattended_default=1 WHERE reader_ref='tmr_T'`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/settings/card-readers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, asRole(req, "manager"))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("manager page = %d: %s", rec.Code, body)
	}
	if n := strings.Count(body, `data-testid="card-reader-unattended"`); n != 2 {
		t.Fatalf("kiosk-default checkboxes = %d, want 2 (cloud row + the ticked sdk row)", n)
	}
	// The ticked sdk row can be cleared.
	rec = postReaderForm(t, mux, "/settings/card-readers/"+findReaderID(t, dp, "tmr_T"), "manager", url.Values{"unattended": {"0"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("clearing a ticked sdk row = %d: %s", rec.Code, rec.Body.String())
	}
	var on int
	if err := dp.Db.QueryRow(`SELECT unattended_default FROM card_readers WHERE reader_ref='tmr_T'`).Scan(&on); err != nil || on != 0 {
		t.Fatalf("unattended_default after clear = %d (%v), want 0", on, err)
	}
}

// ut-docs#6566 (owner order 2026-11-18): with several eligible readers, in any
// provider mix, the picker preselects THIS till's last-used reader; the form's
// hidden reader/method inputs carry it, so one tap on Confirm pays on it.
func TestPicker_PreselectsLastUsedReaderAcrossProviders(t *testing.T) {
	mux, dp := pickerRig(t)
	sumup := seedReaderPlugin(t, dp, "p.sumup", "sumup", true)
	stripe := seedReaderPlugin(t, dp, "p.stripe", "stripe", true)
	s1 := addReader(t, dp, sumup, "SumUp 1", "su_1")
	addReader(t, dp, sumup, "SumUp 2", "su_2")
	addReader(t, dp, sumup, "SumUp 3", "su_3")
	st := addReader(t, dp, stripe, "Stripe 1", "st_1")

	// never used: the first selectable reader is the default.
	if _, got := getPicker(t, mux, "?source=pos"); got.Data.Default != s1.ID || got.Data.Selectable != 4 {
		t.Fatalf("unused till: default=%s selectable=%d, want %s and 4", got.Data.Default, got.Data.Selectable, s1.ID)
	}
	// the Stripe reader was used last: it is preselected in the markup.
	if err := data.NewCardReaderRepo(dp.Db).TouchLastUsed(context.Background(), st.ID); err != nil {
		t.Fatal(err)
	}
	cardreaders.SharedCache().Reset()
	body := getPickerHTML(t, mux, "?source=pos")
	if !strings.Contains(body, `name="reader" value="`+st.ID+`"`) || !strings.Contains(body, `name="method" value="stripe"`) {
		t.Fatalf("hidden inputs must carry the last-used reader %s and method stripe:\n%s", st.ID, body)
	}
	if n := strings.Count(body, `aria-checked="true"`); n != 1 {
		t.Fatalf("exactly one row is preselected, got %d", n)
	}
	// that reader goes busy (in flight elsewhere): never preselect a row that cannot be chosen.
	stripe.state = stateAnswer(map[string]string{"st_1": "busy"})
	cardreaders.SharedCache().Reset()
	if _, got := getPicker(t, mux, "?source=pos"); got.Data.Default == st.ID || got.Data.Default == "" {
		t.Fatalf("a busy last-used reader must not be the default, got %q", got.Data.Default)
	}
}
