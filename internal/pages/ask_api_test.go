package pages

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/ai"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func TestIntArg(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		key  string
		def  int
		min  int
		max  int
		want int
	}{
		{"missing key falls back to default", map[string]any{}, "days", 14, 1, 365, 14},
		{"wrong type (not float64, as a real JSON int would decode) falls back", map[string]any{"days": 7}, "days", 14, 1, 365, 14},
		{"valid float64 in range is used", map[string]any{"days": float64(30)}, "days", 14, 1, 365, 30},
		{"below min falls back to default", map[string]any{"days": float64(0)}, "days", 14, 1, 365, 14},
		{"above max falls back to default", map[string]any{"days": float64(9999)}, "days", 14, 1, 365, 14},
		{"boundary min is accepted", map[string]any{"days": float64(1)}, "days", 14, 1, 365, 1},
		{"boundary max is accepted", map[string]any{"days": float64(365)}, "days", 14, 1, 365, 365},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := intArg(c.args, c.key, c.def, c.min, c.max); got != c.want {
				t.Errorf("intArg(%v, %q, %d, %d, %d) = %d, want %d", c.args, c.key, c.def, c.min, c.max, got, c.want)
			}
		})
	}
}

func newAskAPITestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *sql.DB) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerAskAPI(mux, dp)
	return mux, dp, db
}

// TestAskTools_RunFunctionsCallRealRepoMethodsWithParsedArgs directly
// invokes each askTools() entry's Run function against a real seeded repo
// (bypassing the AI provider entirely, since the tool-use loop itself is
// already thoroughly tested in internal/ai/ask_test.go). This is what
// confirms the wiring -- the right repo method, the right default/parsed
// days & limit -- is correct.
func TestAskTools_RunFunctionsCallRealRepoMethodsWithParsedArgs(t *testing.T) {
	_, _, db := newAskAPITestDeps(t)
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,20,120,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sale_lines(id,sale_id,line_no,name_snapshot,quantity,unit_price,tax_rate_bp,tax_amount,total_before_tax,total_after_tax,item_id) VALUES('l1','s1',1,'Apple',1,100,2000,20,100,120,'itm1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO payments(id,sale_id,method_id,amount,currency,paid_at) VALUES('p1','s1','cash',120,'GBP',datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	repo := data.NewPOSRepo(db)
	tools := map[string]ai.AskTool{}
	for _, tool := range askTools(repo, 0, 0) {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"sales_by_day", "top_items", "payment_breakdown", "stock_levels", "till_activity_summary"} {
		if _, ok := tools[name]; !ok {
			t.Fatalf("expected a %q tool to be registered", name)
		}
	}

	salesByDay, err := tools["sales_by_day"].Run(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("sales_by_day: %v", err)
	}
	rows, ok := salesByDay.([]data.DailySales)
	if !ok || len(rows) != 1 || rows[0].Total != 120 {
		t.Fatalf("sales_by_day (default days): got %+v", salesByDay)
	}

	// An out-of-range days value falls back to the 14-day default rather
	// than erroring or querying an unbounded window.
	salesByDayBad, err := tools["sales_by_day"].Run(ctx, map[string]any{"days": float64(99999)})
	if err != nil {
		t.Fatalf("sales_by_day (bad days): %v", err)
	}
	if rows2 := salesByDayBad.([]data.DailySales); len(rows2) != 1 || rows2[0].Total != 120 {
		t.Fatalf("sales_by_day (bad days should still use the 14-day default): got %+v", salesByDayBad)
	}

	top, err := tools["top_items"].Run(ctx, map[string]any{"days": float64(7), "limit": float64(5)})
	if err != nil {
		t.Fatalf("top_items: %v", err)
	}
	if items := top.([]data.TopItem); len(items) != 1 || items[0].Name != "Apple" {
		t.Fatalf("top_items: got %+v", top)
	}

	pay, err := tools["payment_breakdown"].Run(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("payment_breakdown: %v", err)
	}
	if methods := pay.([]data.MethodTotal); len(methods) != 1 || methods[0].Method != "cash" {
		t.Fatalf("payment_breakdown: got %+v", pay)
	}

	stock, err := tools["stock_levels"].Run(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("stock_levels: %v", err)
	}
	levels, ok := stock.([]data.LowStockItem)
	if !ok || len(levels) != 1 || levels[0].ItemID != "itm1" {
		t.Fatalf("stock_levels: got %+v", stock)
	}

	if _, err := tools["till_activity_summary"].Run(ctx, map[string]any{}); err != nil {
		t.Fatalf("till_activity_summary: %v", err)
	}
}

// tillNameOrDefault reads this (primary) till's own name — distinct from a
// replica's own sync.till_name — defaulting when unset (ut-docs#396).
func TestTillNameOrDefault(t *testing.T) {
	_, dp, _ := newAskAPITestDeps(t)
	ctx := t.Context()

	if got := tillNameOrDefault(ctx, dp, "en"); got != "Till 1" {
		t.Fatalf("unset till.name default = %q, want %q", got, "Till 1")
	}

	if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
		t.Fatalf("set till.name: %v", err)
	}
	if got := tillNameOrDefault(ctx, dp, "en"); got != "Front Counter" {
		t.Fatalf("set till.name = %q, want %q", got, "Front Counter")
	}

	// Blank/whitespace-only is treated the same as unset.
	if err := dp.Settings.Set(ctx, "till.name", "   "); err != nil {
		t.Fatalf("set blank till.name: %v", err)
	}
	if got := tillNameOrDefault(ctx, dp, "en"); got != "Till 1" {
		t.Fatalf("blank till.name = %q, want default %q", got, "Till 1")
	}
}

// The default must go through T, not a bare English literal (independent
// review, ut-docs#396) — otherwise an upgraded install with no till.name set
// shows "Till 1" in Latin script on an RTL Farsi/Arabic till, contradicting
// the wizard's own translated prefill and the translated manual topics.
func TestTillNameOrDefault_TranslatesPerLocale(t *testing.T) {
	_, dp, _ := newAskAPITestDeps(t)
	ctx := t.Context()

	if got := tillNameOrDefault(ctx, dp, "fa"); got != "صندوق ۱" {
		t.Fatalf("unset till.name default (fa) = %q, want %q", got, "صندوق ۱")
	}
	if got := tillNameOrDefault(ctx, dp, "ar"); got != "الصندوق 1" {
		t.Fatalf("unset till.name default (ar) = %q, want %q", got, "الصندوق 1")
	}

	// A set name is never translated, in any locale.
	if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
		t.Fatalf("set till.name: %v", err)
	}
	if got := tillNameOrDefault(ctx, dp, "fa"); got != "Front Counter" {
		t.Fatalf("set till.name (fa locale) = %q, want %q", got, "Front Counter")
	}
}

// TestStockLevelsToolReturnsSnakeCaseJSON guards ut-docs#277: the
// stock_levels AI tool's []data.LowStockItem result is marshalled to JSON
// (by the AI provider's tool-call plumbing) and must carry this repo's
// snake_case wire convention (universal-till/CLAUDE.md), not Go's default
// PascalCase field names.
func TestStockLevelsToolReturnsSnakeCaseJSON(t *testing.T) {
	_, _, db := newAskAPITestDeps(t)
	repo := data.NewPOSRepo(db)
	var stockLevels ai.AskTool
	for _, tool := range askTools(repo, 0, 0) {
		if tool.Name == "stock_levels" {
			stockLevels = tool
		}
	}
	if stockLevels.Run == nil {
		t.Fatal("expected a stock_levels tool to be registered")
	}

	result, err := stockLevels.Run(t.Context(), map[string]any{})
	if err != nil {
		t.Fatalf("stock_levels: %v", err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal stock_levels result: %v", err)
	}

	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal stock_levels JSON: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 stock level row, got %d: %s", len(decoded), raw)
	}

	wantKeys := []string{"item_id", "name", "sku", "location_id", "location_name", "current_qty", "reorder_level", "lead_time_days"}
	for _, key := range wantKeys {
		if _, ok := decoded[0][key]; !ok {
			t.Errorf("expected snake_case key %q in stock_levels JSON, got %s", key, raw)
		}
	}

	pascalKeys := []string{"ItemID", "Name", "SKU", "LocationID", "LocationName", "CurrentQty", "ReorderLevel", "LeadTimeDays"}
	for _, key := range pascalKeys {
		if _, ok := decoded[0][key]; ok {
			t.Errorf("stock_levels JSON leaked PascalCase key %q against this repo's snake_case convention: %s", key, raw)
		}
	}
}

// TestReportsAITools_ReturnSnakeCaseJSON guards ut-docs#322 (same defect
// class as ut-docs#277, fixed above for stock_levels): data.DailySales,
// data.TopItem and data.MethodTotal had no json tags, so the sales_by_day,
// top_items and payment_breakdown AI tools serialized Go's default
// PascalCase field names, against this repo's snake_case wire convention
// (universal-till/CLAUDE.md).
func TestReportsAITools_ReturnSnakeCaseJSON(t *testing.T) {
	_, _, db := newAskAPITestDeps(t)
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,20,120,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sale_lines(id,sale_id,line_no,name_snapshot,quantity,unit_price,tax_rate_bp,tax_amount,total_before_tax,total_after_tax,item_id) VALUES('l1','s1',1,'Apple',1,100,2000,20,100,120,'itm1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO payments(id,sale_id,method_id,amount,currency,paid_at) VALUES('p1','s1','cash',120,'GBP',datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	repo := data.NewPOSRepo(db)
	tools := map[string]ai.AskTool{}
	for _, tool := range askTools(repo, 0, 0) {
		tools[tool.Name] = tool
	}

	cases := []struct {
		tool       string
		wantKeys   []string
		pascalKeys []string
	}{
		{"sales_by_day", []string{"day", "count", "total", "tax_total"}, []string{"Day", "Count", "Total", "TaxTotal"}},
		{"top_items", []string{"name", "qty", "revenue"}, []string{"Name", "Qty", "Revenue"}},
		{"payment_breakdown", []string{"method", "count", "amount"}, []string{"Method", "Count", "Amount"}},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			result, err := tools[c.tool].Run(ctx, map[string]any{})
			if err != nil {
				t.Fatalf("%s: %v", c.tool, err)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal %s result: %v", c.tool, err)
			}
			var decoded []map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("unmarshal %s JSON: %v", c.tool, err)
			}
			if len(decoded) != 1 {
				t.Fatalf("expected 1 %s row, got %d: %s", c.tool, len(decoded), raw)
			}
			for _, key := range c.wantKeys {
				if _, ok := decoded[0][key]; !ok {
					t.Errorf("expected snake_case key %q in %s JSON, got %s", key, c.tool, raw)
				}
			}
			for _, key := range c.pascalKeys {
				if _, ok := decoded[0][key]; ok {
					t.Errorf("%s JSON leaked PascalCase key %q against this repo's snake_case convention: %s", c.tool, key, raw)
				}
			}
		})
	}
}

func TestAskAPI_NotConfiguredReturns404(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newAskAPITestDeps(t)
	// No AI plugin active and no UT_AI_* env -- svc.CanAsk() is false. With
	// UT_AUTH=off the manager gate always passes, so 404 here is already
	// unambiguous proof of the CanAsk gate (nothing else in the handler
	// returns 404) -- but it doesn't by itself prove CanAsk runs BEFORE the
	// manager check. The second request below does: auth ON, no manager
	// context, not configured -- if the handler checked manager first this
	// would be 403, not 404.
	req := httptest.NewRequest(http.MethodPost, "/api/reports/ask", strings.NewReader("question=how did we do today?"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when ask isn't configured, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAskAPI_NotConfiguredReturns404BeforeManagerCheck(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, _, _ := newAskAPITestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/reports/ask", strings.NewReader("question=how did we do today?"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 (CanAsk checked before the manager gate) even for a non-manager, got %d: %s", rec.Code, rec.Body.String())
	}
}

// fakeAskServer mimics the minimal single-turn Ollama /api/chat response
// shape used by internal/ai/ask_test.go's askService helper -- enough to
// make svc.CanAsk() true and exercise a real Ask() round trip, without
// re-testing the tool-use loop itself (already covered there).
func fakeAskServer(t *testing.T, answer string, status int) *ai.Service {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"role": "assistant", "content": answer},
		})
	}))
	t.Cleanup(srv.Close)
	return ai.New(ai.Config{Provider: "ollama", Endpoint: srv.URL, Model: "vision", AskModel: "text"})
}

func TestAskAPI_RequiresManagerWhenConfigured(t *testing.T) {
	// Set explicitly (not just left ambient/unset) so this test can't
	// silently pass or fail depending on the developer's shell environment.
	t.Setenv("UT_AUTH", "on")
	mux, dp, _ := newAskAPITestDeps(t)
	dp.AI = fakeAskServer(t, "irrelevant", http.StatusOK)
	req := httptest.NewRequest(http.MethodPost, "/api/reports/ask", strings.NewReader("question=how did we do today?"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-manager, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAskAPI_ValidatesQuestionLength(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newAskAPITestDeps(t)
	dp.AI = fakeAskServer(t, "irrelevant", http.StatusOK)

	rec := postForm(mux, "/api/reports/ask", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an empty question, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = postForm(mux, "/api/reports/ask", url.Values{"question": {"   "}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a whitespace-only question (the TrimSpace path), got %d: %s", rec.Code, rec.Body.String())
	}

	rec = postForm(mux, "/api/reports/ask", url.Values{"question": {strings.Repeat("a", 501)}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a >500 char question, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAskAPI_SuccessRendersAnswerAndAudits(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, db := newAskAPITestDeps(t)
	dp.AI = fakeAskServer(t, "You took £123.45 today.", http.StatusOK)

	req := httptest.NewRequest(http.MethodPost, "/api/reports/ask", strings.NewReader("question=how did we do today?"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "£123.45") {
		t.Fatalf("expected the model's answer rendered, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "muted") {
		t.Fatalf("expected a non-error answer, but got the error styling: %s", rec.Body.String())
	}

	var payload string
	if err := db.QueryRow(`SELECT data_json FROM audit_log WHERE action='ai_ask'`).Scan(&payload); err != nil {
		t.Fatalf("expected an ai_ask audit row: %v", err)
	}
	if !strings.Contains(payload, `"ok":true`) {
		t.Fatalf("expected the audit payload to record ok:true, got: %s", payload)
	}
	if !strings.Contains(payload, "how did we do today?") {
		t.Fatalf("expected the audit payload to record the question, got: %s", payload)
	}
}

func TestAskAPI_ProviderErrorRendersErrorAnswerAndAudits(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, db := newAskAPITestDeps(t)
	dp.AI = fakeAskServer(t, "", http.StatusInternalServerError)

	req := httptest.NewRequest(http.MethodPost, "/api/reports/ask", strings.NewReader("question=how did we do today?"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place error message, not an HTTP error), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "muted") {
		t.Fatalf("expected the error styling on a failed ask, got: %s", rec.Body.String())
	}

	var payload string
	if err := db.QueryRow(`SELECT data_json FROM audit_log WHERE action='ai_ask'`).Scan(&payload); err != nil {
		t.Fatalf("expected an ai_ask audit row even on failure: %v", err)
	}
	if !strings.Contains(payload, `"ok":false`) {
		t.Fatalf("expected the audit payload to record ok:false, got: %s", payload)
	}
	if !strings.Contains(payload, "how did we do today?") {
		t.Fatalf("expected the audit payload to record the question even on failure, got: %s", payload)
	}
}

// A 200 response with an empty answer is a distinct failure mode from a
// non-200 status: ollama_ask.go's chat() treats an empty model response as
// an error too ("empty model response"), not a silent success.
func TestAskAPI_EmptyAnswerWith200TreatedAsFailure(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, db := newAskAPITestDeps(t)
	dp.AI = fakeAskServer(t, "", http.StatusOK)

	req := httptest.NewRequest(http.MethodPost, "/api/reports/ask", strings.NewReader("question=how did we do today?"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place error message, not an HTTP error), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "muted") {
		t.Fatalf("expected the error styling for an empty model response, got: %s", rec.Body.String())
	}

	var payload string
	if err := db.QueryRow(`SELECT data_json FROM audit_log WHERE action='ai_ask'`).Scan(&payload); err != nil {
		t.Fatalf("expected an ai_ask audit row: %v", err)
	}
	if !strings.Contains(payload, `"ok":false`) {
		t.Fatalf("expected the audit payload to record ok:false for an empty response, got: %s", payload)
	}
}
