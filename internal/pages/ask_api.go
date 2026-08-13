package pages

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/ai"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// intArg reads a bounded integer tool argument (JSON numbers arrive as
// float64); out-of-range or missing values fall back to def.
func intArg(args map[string]any, key string, def, min, max int) int {
	f, ok := args[key].(float64)
	if !ok {
		return def
	}
	n := int(f)
	if n < min || n > max {
		return def
	}
	return n
}

// daysArgWindow reads the bounded "days" tool argument and turns it into a
// simple rolling [from, to) window ending now — these ask-tool calls only
// ever need "the last N days", not calendar-period awareness, so this stays
// separate from parseReportWindow's business-day-boundary logic.
func daysArgWindow(args map[string]any, key string, def int) (time.Time, time.Time) {
	days := intArg(args, key, def, 1, 365)
	// +1s pad: SQL window comparisons truncate to whole seconds, so an
	// exact-now exclusive upper bound can drop a sale committed in this
	// same wall-clock second (see reportNow's doc comment in
	// reports_page.go).
	to := time.Now().Add(time.Second)
	return to.Add(-time.Duration(days) * 24 * time.Hour), to
}

// askTools is the read-only tool surface for "Ask your till"
// (ai-integration.md §a). Data red-line: identity-free aggregates only —
// no customer data is wired, and the audit tool returns counts, not payloads.
// hh, mm is the shop's resolved business-day-start (parseBusinessDayStart),
// so sales_by_day's grouping agrees with /reports' own boundary (ut-docs#559)
// instead of always grouping on local midnight.
func askTools(repo *data.POSRepo, hh, mm int) []ai.AskTool {
	days := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"days": map[string]any{"type": "integer", "description": "how many days back to look (1-365)"},
		},
	}
	return []ai.AskTool{
		{
			Name:        "sales_by_day",
			Description: "Daily sales totals: per day the number of completed sales, revenue total and tax total (minor units). Returns/refunds are excluded, not netted.",
			Params:      days,
			Run: func(ctx context.Context, args map[string]any) (any, error) {
				from, to := daysArgWindow(args, "days", 14)
				return repo.SalesByDay(ctx, from, to, hh, mm)
			},
		},
		{
			Name:        "top_items",
			Description: "Best-selling items over a period: name, quantity sold, revenue (minor units). Returns/refunds are excluded.",
			Params: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"days":  map[string]any{"type": "integer", "description": "how many days back to look (1-365)"},
					"limit": map[string]any{"type": "integer", "description": "how many items to return (1-50)"},
				},
			},
			Run: func(ctx context.Context, args map[string]any) (any, error) {
				from, to := daysArgWindow(args, "days", 14)
				return repo.TopItems(ctx, from, to, intArg(args, "limit", 10, 1, 50))
			},
		},
		{
			Name:        "payment_breakdown",
			Description: "Takings per payment method over a period: method, transaction count, amount taken (minor units).",
			Params:      days,
			Run: func(ctx context.Context, args map[string]any) (any, error) {
				from, to := daysArgWindow(args, "days", 14)
				return repo.PaymentBreakdown(ctx, from, to)
			},
		},
		{
			Name:        "stock_levels",
			Description: "Current stock levels per item and location, including reorder levels and lead time (days to receive a reorder). Use for questions about stock, low stock and reordering.",
			Params:      map[string]any{"type": "object", "properties": map[string]any{}},
			Run: func(ctx context.Context, args map[string]any) (any, error) {
				return repo.ListStockLevels(ctx)
			},
		},
		{
			Name:        "till_activity_summary",
			Description: "Counts of till audit actions per operator over a period (logins, voids, no-sales, stock overrides…). Use for questions about staff activity or anything suspicious.",
			Params:      days,
			Run: func(ctx context.Context, args map[string]any) (any, error) {
				return repo.AuditActionSummary(ctx, intArg(args, "days", 14, 1, 365), 100)
			},
		},
	}
}

// registerAskAPI mounts the "Ask your till" endpoint (manager/admin; only
// live when the AI provider supports the tool loop). Never on the sale path.
func registerAskAPI(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	answer := func(w http.ResponseWriter, r *http.Request, text string, isErr bool) {
		httpx.RenderPartial("ui/partials/ask_answer.html", map[string]any{
			"text": text, "isError": isErr,
		})(w, r)
	}

	mux.HandleFunc("POST /api/reports/ask", func(w http.ResponseWriter, r *http.Request) {
		svc := aiService(r.Context(), d)
		if !svc.CanAsk() {
			http.Error(w, "ask is not configured", http.StatusNotFound)
			return
		}
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		question := strings.TrimSpace(r.Form.Get("question"))
		if question == "" || len(question) > 500 {
			http.Error(w, "question must be 1-500 characters", http.StatusBadRequest)
			return
		}
		cur := httpx.ActiveCurrency()
		locale := httpx.ResolveLocale(w, r)
		shop := ai.ShopContext{
			StoreName:        storeNameOrDefault(r.Context(), d),
			CurrencyCode:     cur.Code,
			CurrencyDecimals: cur.Decimals,
			Locale:           locale,
		}
		bizDayStart, _, _ := d.Settings.Get(r.Context(), keyReportsBusinessDayStart)
		hh, mm := parseBusinessDayStart(bizDayStart)
		text, err := svc.Ask(r.Context(), question, shop, askTools(posRepo, hh, mm))
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "ai", "-", "ai_ask",
			map[string]any{"question": question, "ok": err == nil}, now, "")
		if err != nil {
			answer(w, r, httpx.T(locale, "reports.ask.error"), true)
			return
		}
		answer(w, r, text, false)
	})
}

// storeNameOrDefault reads the shop name saved by the first-boot wizard.
func storeNameOrDefault(ctx context.Context, d *common.Deps) string {
	if v, ok, _ := d.Settings.Get(ctx, "store.name"); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return "this shop"
}

// tillNameOrDefault reads this (primary) till's own name, defaulting when
// unset — distinct from a replica's own sync.till_name (ut-docs#396).
// Unlike storeNameOrDefault's bare-string default (which only ever reaches
// non-template contexts — receipts, mDNS, AI prompts), every caller of this
// helper renders into a template that already shows the wizard's translated
// "setup.till_name.default" — so the fallback here goes through T too,
// otherwise an upgraded install with no till.name set would show the English
// "Till 1" on a Farsi/Arabic/Turkish till (review finding, ut-docs#396).
func tillNameOrDefault(ctx context.Context, d *common.Deps, locale string) string {
	if v, ok, _ := d.Settings.Get(ctx, "till.name"); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return httpx.T(locale, "setup.till_name.default")
}
