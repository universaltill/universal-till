package catalogpull

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// fakeFeed is an httptest stand-in for ut-cloud's GET
// /api/v1/stores/catalog (contract cloud-catalog-feed.md 1.0.0): rows in
// version order, pages cut at version boundaries, next/high_water as the
// contract defines them. Knobs inject the error answers and odd pages.
type fakeFeed struct {
	mu    sync.Mutex
	rows  []feedRow
	high  int64
	limit int // page cap the fake applies (the till's limit is ignored when > 0)

	status int    // when set, every request answers this status …
	code   string // … with this error code
	// goneOnce answers the next request with since > 0 by 410 resync.
	goneOnce bool
	// empty maps a since to the next of an EMPTY page served for it.
	empty map[int64]int64
	// stuck serves an empty page with next == since forever.
	stuck bool

	sinces []int64
}

type feedRow struct {
	Kind    string         `json:"kind"`
	CloudID string         `json:"cloud_id"`
	Version int64          `json:"version"`
	Deleted bool           `json:"deleted"`
	Data    map[string]any `json:"data,omitempty"`
}

func (f *fakeFeed) add(r feedRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, r)
	sort.SliceStable(f.rows, func(i, j int) bool { return f.rows[i].Version < f.rows[j].Version })
	if r.Version > f.high {
		f.high = r.Version
	}
}

// drop removes every row of cloudID (a purged tombstone's row).
func (f *fakeFeed) drop(cloudID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.rows[:0]
	for _, r := range f.rows {
		if r.CloudID != cloudID {
			out = append(out, r)
		}
	}
	f.rows = out
}

func (f *fakeFeed) requests() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.sinces...)
}

func writeErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": map[string]string{"code": code, "message": code}})
}

func (f *fakeFeed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/stores/catalog" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok-1" {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	q := r.URL.Query()
	if q.Get("store_id") != "store-1" {
		writeErr(w, http.StatusBadRequest, "invalid_request")
		return
	}
	since, err := strconv.ParseInt(q.Get("since"), 10, 64)
	if err != nil || since < 0 {
		writeErr(w, http.StatusBadRequest, "invalid_request")
		return
	}
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit < 1 || limit > 500 {
		writeErr(w, http.StatusBadRequest, "invalid_request")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sinces = append(f.sinces, since)
	if f.status != 0 {
		writeErr(w, f.status, f.code)
		return
	}
	if f.goneOnce && since > 0 {
		f.goneOnce = false
		writeErr(w, http.StatusGone, "resync")
		return
	}
	if since > f.high {
		writeErr(w, http.StatusGone, "resync")
		return
	}
	write := func(rows []feedRow, next int64) {
		if rows == nil {
			rows = []feedRow{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"rows": rows, "next": next, "high_water": f.high}, "error": nil})
	}
	if f.stuck {
		write(nil, since)
		return
	}
	if next, ok := f.empty[since]; ok {
		write(nil, next)
		return
	}
	if f.limit > 0 {
		limit = f.limit
	}
	var avail []feedRow
	for _, row := range f.rows {
		if row.Version > since && row.Version <= f.high {
			avail = append(avail, row)
		}
	}
	var page []feedRow
	for i := 0; i < len(avail); {
		j := i
		for j < len(avail) && avail[j].Version == avail[i].Version {
			j++
		}
		if len(page) > 0 && len(page)+(j-i) > limit {
			break
		}
		page = append(page, avail[i:j]...)
		i = j
	}
	next := f.high
	if len(page) < len(avail) {
		next = page[len(page)-1].Version
	}
	write(page, next)
}

// harness is one main till's database plus a fake cloud feed.
type harness struct {
	t     *testing.T
	db    *sql.DB
	feed  *fakeFeed
	srv   *httptest.Server
	fetch Fetcher
	opt   Options
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "till.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	feed := &fakeFeed{}
	srv := httptest.NewServer(feed)
	t.Cleanup(srv.Close)
	return &harness{
		t: t, db: d.DB, feed: feed, srv: srv,
		fetch: NewHTTPFetcher(srv.Client(), srv.URL, "store-1", "tok-1"),
	}
}

func (h *harness) run() (Result, error) {
	h.t.Helper()
	return Run(context.Background(), h.db, h.fetch, h.opt)
}

func (h *harness) mustRun() Result {
	h.t.Helper()
	res, err := h.run()
	if err != nil {
		h.t.Fatalf("Run: %v", err)
	}
	return res
}

func (h *harness) setting(key string) string {
	h.t.Helper()
	v, _, err := data.NewSettingsRepo(h.db).Get(context.Background(), key)
	if err != nil {
		h.t.Fatal(err)
	}
	return v
}

func (h *harness) cursor() int64 {
	h.t.Helper()
	v := h.setting(KeyCursor)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		h.t.Fatalf("cursor %q: %v", v, err)
	}
	return n
}

func (h *harness) count(q string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.db.QueryRow(q, args...).Scan(&n); err != nil {
		h.t.Fatalf("%s: %v", q, err)
	}
	return n
}

type itemRow struct {
	Name, SKU, Desc, Unit, Category, TaxCode, Color, ItemType  string
	Price                                                      int64
	Active, Weighed, Untracked, AgeRestricted, Hidden, Removed bool
	NQValue                                                    sql.NullInt64
	NQUnit                                                     sql.NullString
}

func (h *harness) item(id string) (itemRow, bool) {
	h.t.Helper()
	var it itemRow
	err := h.db.QueryRow(`SELECT name, COALESCE(sku,''), COALESCE(description,''), unit, COALESCE(category_id,''), COALESCE(tax_code_id,''), COALESCE(color,''), item_type, base_price,
		is_active, is_weighed, stock_untracked, age_restricted, sell_screen_hidden, sell_screen_removed, net_quantity_value, net_quantity_unit FROM items WHERE id = ?`, id).
		Scan(&it.Name, &it.SKU, &it.Desc, &it.Unit, &it.Category, &it.TaxCode, &it.Color, &it.ItemType, &it.Price,
			&it.Active, &it.Weighed, &it.Untracked, &it.AgeRestricted, &it.Hidden, &it.Removed, &it.NQValue, &it.NQUnit)
	if errors.Is(err, sql.ErrNoRows) {
		return itemRow{}, false
	}
	if err != nil {
		h.t.Fatalf("item %s: %v", id, err)
	}
	return it, true
}

// sell records a completed sale of itemID, so it was "ever sold".
func (h *harness) sell(itemID string) {
	h.t.Helper()
	sid := "sale-" + itemID
	if _, err := h.db.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES (?, ?, 'completed', 'sale', 'GBP', 250, 0, 0, 250, datetime('now'))`, sid, "R-"+sid); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES (?, ?, 1, ?, 'Item', 1, 250, 0, 0, 0, 250, 250)`, "l-"+sid, sid, itemID); err != nil {
		h.t.Fatal(err)
	}
}

// Fixed cloud ids for readability.
const (
	cTax    = "11111111-1111-4111-8111-111111111111"
	cTax2   = "11111111-1111-4111-8111-222222222222"
	cCat    = "22222222-2222-4222-8222-111111111111"
	cSubCat = "22222222-2222-4222-8222-222222222222"
)

func itemID(n int) string {
	return "33333333-3333-4333-8333-" + strconv.FormatInt(int64(100000000000+n), 10)
}

func taxRow(v int64) feedRow {
	return feedRow{Kind: "tax_code", CloudID: cTax, Version: v, Data: map[string]any{
		"name": "Standard", "rate_basis_points": 2000, "takeaway_rate_basis_points": nil, "active": true,
	}}
}

func catRow(id string, v int64, name string, parent any) feedRow {
	return feedRow{Kind: "category", CloudID: id, Version: v, Data: map[string]any{
		"name": name, "parent_cloud_id": parent, "sort_order": 3, "color": "", "icon": "",
		"sell_screen_hidden": false, "active": true, "modifier_group_cloud_ids": []string{},
	}}
}

func itemRowData(name string, price int64) map[string]any {
	return map[string]any{
		"name": name, "sku": "", "description": "", "unit": "each", "price_minor": price,
		"category_cloud_id": nil, "tax_code_cloud_id": nil, "active": true, "is_weighed": false,
		"stock_untracked": false, "age_restricted": false, "color": "", "icon": "", "item_type": "standard",
		"sell_screen_hidden": false, "sell_screen_removed": false, "net_quantity_value": nil, "net_quantity_unit": nil,
		"modifier_group_cloud_ids": []string{}, "modifier_opt_out_cloud_ids": []string{}, "option_set_cloud_ids": []string{},
	}
}

func itemFeedRow(n int, v int64, name string, price int64) feedRow {
	return feedRow{Kind: "item", CloudID: itemID(n), Version: v, Data: itemRowData(name, price)}
}

func setCommitChunk(n int) func() {
	old := commitChunk
	commitChunk = n
	return func() { commitChunk = old }
}

func setMaxPages(n int) func() {
	old := maxPagesPerRun
	maxPagesPerRun = n
	return func() { maxPagesPerRun = old }
}

func setAfterChunkCommit(f func()) func() {
	old := afterChunkCommit
	afterChunkCommit = f
	return func() { afterChunkCommit = old }
}

func setBeforeChunkCommit(f func() error) func() {
	old := beforeChunkCommit
	beforeChunkCommit = f
	return func() { beforeChunkCommit = old }
}
