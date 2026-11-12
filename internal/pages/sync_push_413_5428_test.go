package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5428: a primary that answers 413 above a body size.

type sizeFake struct {
	limit atomic.Int64
	mu    sync.Mutex
	reqs  [][]string // sale ids per POST /api/sync/sales
}

func (f *sizeFake) requests() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.reqs...)
}

func (f *sizeFake) reset() {
	f.mu.Lock()
	f.reqs = nil
	f.mu.Unlock()
}

func newSizeLimitedPrimary(t *testing.T, primary http.Handler) (*httptest.Server, *sizeFake) {
	t.Helper()
	f := &sizeFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sync/sales" {
			primary.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var js []journalSale
		_ = json.Unmarshal(body, &js)
		ids := make([]string, 0, len(js))
		for _, j := range js {
			ids = append(ids, j.Sale.ID)
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, ids)
		f.mu.Unlock()
		if int64(len(body)) > f.limit.Load() {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		primary.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

type pushRig struct {
	primaryDp, replicaDp *common.Deps
	fake                 *sizeFake
	srv                  *httptest.Server
	client               *http.Client
	oneSale              int // bytes of one journal entry
}

func newPushRig(t *testing.T) *pushRig {
	t.Helper()
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	ctx := context.Background()
	mux, pdp := newSyncSalesTestDeps(t)
	if _, err := data.NewTillsRepo(pdp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-abc")); err != nil {
		t.Fatal(err)
	}
	srv, fake := newSizeLimitedPrimary(t, mux)
	_, rdp := newSyncSalesTestDeps(t)
	for k, v := range map[string]string{"sync.primary_url": srv.URL, "sync.bearer": "token-abc"} {
		if err := rdp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	r := &pushRig{primaryDp: pdp, replicaDp: rdp, fake: fake, srv: srv, client: &http.Client{Timeout: 10 * time.Second}}
	// Measure one journal so limits don't depend on byte counts.
	seedLocalSale(t, rdp, "m-001", "R-m-001", "2025-12-31T00:00:00Z")
	repo := data.NewPOSRepo(rdp.Db)
	j, _, err := buildJournal(ctx, repo, "R-m-001")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(j)
	r.oneSale = len(raw) + 1
	if _, err := rdp.Db.ExecContext(ctx, `DELETE FROM payments WHERE sale_id='m-001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := rdp.Db.ExecContext(ctx, `DELETE FROM sale_lines WHERE sale_id='m-001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := rdp.Db.ExecContext(ctx, `DELETE FROM sales WHERE id='m-001'`); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *pushRig) seed(t *testing.T, n int) []string {
	t.Helper()
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("p-%03d", i+1)
		seedLocalSale(t, r.replicaDp, ids[i], "R-"+ids[i], base.Add(time.Duration(i)*time.Second).Format(time.RFC3339))
	}
	return ids
}

func (r *pushRig) seedBig(t *testing.T, id string, at time.Time, extra int) {
	t.Helper()
	seedLocalSale(t, r.replicaDp, id, "R-"+id, at.Format(time.RFC3339))
	if _, err := r.replicaDp.Db.ExecContext(context.Background(),
		`UPDATE sale_lines SET name_snapshot = ? WHERE sale_id = ?`, strings.Repeat("x", extra), id); err != nil {
		t.Fatal(err)
	}
}

func (r *pushRig) tick() { syncPushTick(context.Background(), r.replicaDp, r.client) }

func (r *pushRig) setting(k string) string {
	v, _, _ := r.replicaDp.Settings.Get(context.Background(), k)
	return v
}

func (r *pushRig) queued(t *testing.T) int {
	t.Helper()
	n, err := countQueuedSales(context.Background(), data.NewPOSRepo(r.replicaDp.Db), r.setting("sync.push_cursor"))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (r *pushRig) onPrimary(t *testing.T, id string) bool {
	t.Helper()
	return primarySaleCount(t, r.primaryDp, id) == 1
}

func noKicks(t *testing.T) *int {
	t.Helper()
	n := 0
	prev := salesPushRekick
	salesPushRekick = func(*common.Deps, time.Duration) { n++ }
	t.Cleanup(func() { salesPushRekick = prev })
	return &n
}

func TestSyncPush413_DrainsInOrderAndClearsQueue(t *testing.T) {
	r := newPushRig(t)
	ids := r.seed(t, 50)
	r.fake.limit.Store(int64(3 * r.oneSale))
	for i := 0; i < 30 && r.queued(t) > 0; i++ {
		r.tick()
	}
	if q := r.queued(t); q != 0 {
		t.Fatalf("queue not drained: %d", q)
	}
	for _, id := range ids {
		if !r.onPrimary(t, id) {
			t.Fatalf("%s missing on primary", id)
		}
	}
	if want := "2026-01-01T10:00:49Z|p-050"; r.setting("sync.push_cursor") != want {
		t.Fatalf("cursor = %q, want %q", r.setting("sync.push_cursor"), want)
	}
	// First occurrence order, and no request carried a sale while an earlier
	// one had not yet had a 200 (each request starts at the cursor).
	seen := map[string]bool{}
	var order []string
	for _, req := range r.fake.requests() {
		for _, id := range req {
			if !seen[id] {
				seen[id] = true
				order = append(order, id)
			}
		}
	}
	if fmt.Sprint(order) != fmt.Sprint(ids) {
		t.Fatalf("first-seen order = %v", order)
	}
	if logging.HasOpenProblem(SalePushTooLargeProblemKey) {
		t.Fatal("no problem expected")
	}
}

func TestSyncPush413_CursorAdvancesPerSubBatch(t *testing.T) {
	r := newPushRig(t)
	r.seed(t, 50)
	r.fake.limit.Store(int64(25*r.oneSale + 100))
	// Stop after the first accepted request by closing the fake.
	orig := r.srv.Config.Handler
	var accepted atomic.Int32
	r.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if accepted.Load() > 0 && req.URL.Path == "/api/sync/sales" {
			http.Error(w, "gone", http.StatusServiceUnavailable)
			return
		}
		orig.ServeHTTP(w, req)
		if req.URL.Path == "/api/sync/sales" && int64(req.ContentLength) <= r.fake.limit.Load() {
			accepted.Add(1)
		}
	})
	r.tick()
	if want := "2026-01-01T10:00:24Z|p-025"; r.setting("sync.push_cursor") != want {
		t.Fatalf("cursor = %q, want %q", r.setting("sync.push_cursor"), want)
	}
	r.fake.reset()
	r.srv.Config.Handler = orig
	r.tick()
	reqs := r.fake.requests()
	if len(reqs) == 0 || reqs[0][0] != "p-026" {
		t.Fatalf("next tick should start at p-026, got %v", reqs)
	}
}

func TestSyncPush413_SubBatchRespectsSettleWindow(t *testing.T) {
	r := newPushRig(t)
	base := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("w-%d", i)
		seedLocalSale(t, r.replicaDp, id, "R-"+id, base.Add(time.Duration(i)*time.Second).Format(time.RFC3339))
	}
	// "now" is 1s after sale 4: sales 3 and 4 are inside the window? cutoff = now-5s.
	now := base.Add(4*time.Second + 2*time.Second) // cutoff = base+1s... settled: w-1 only
	prev := salesPushNow
	salesPushNow = func() time.Time { return now }
	t.Cleanup(func() { salesPushNow = prev })
	noKicks(t)
	r.fake.limit.Store(int64(2*r.oneSale + 50))
	r.tick()
	// 4 -> 2 accepted twice; cutoff = base+1s so only w-1 is settled.
	if want := base.Add(time.Second).Format(time.RFC3339) + "|w-1"; r.setting("sync.push_cursor") != want {
		t.Fatalf("cursor = %q, want %q", r.setting("sync.push_cursor"), want)
	}
}

func TestSyncPush413_BoundedPerTickAndLearnsSize(t *testing.T) {
	r := newPushRig(t)
	r.seed(t, 50)
	kicks := noKicks(t)
	r.fake.limit.Store(1) // always 413
	r.tick()
	if n := len(r.fake.requests()); n != 7 {
		t.Fatalf("requests in one tick = %d, want 7", n)
	}
	if *kicks != 0 {
		t.Fatalf("no rekick on 413, got %d", *kicks)
	}
	if got := r.setting("sync.push_batch_size"); got != "1" {
		t.Fatalf("learned size = %q, want 1", got)
	}
	r.fake.reset()
	r.tick()
	reqs := r.fake.requests()
	if len(reqs) != 1 || len(reqs[0]) != 1 {
		t.Fatalf("next tick should send one sale once, got %v", reqs)
	}
}

func TestSyncPush413_LearnedSizeGrowsBack(t *testing.T) {
	r := newPushRig(t)
	r.seed(t, 20)
	noKicks(t)
	if err := r.replicaDp.Settings.Set(context.Background(), "sync.push_batch_size", "4"); err != nil {
		t.Fatal(err)
	}
	r.fake.limit.Store(1 << 30)
	r.tick()
	if got := r.setting("sync.push_batch_size"); got != "8" {
		t.Fatalf("learned size = %q, want 8", got)
	}
}

func TestSyncPush413_OtherStatusDoesNotHalve(t *testing.T) {
	r := newPushRig(t)
	r.seed(t, 10)
	r.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	r.tick()
	if got := r.setting("sync.push_batch_size"); got != "" {
		t.Fatalf("learned size = %q, want unset", got)
	}
	if r.setting("sync.push_cursor") != "" {
		t.Fatal("cursor moved on 500")
	}
}

// Review: a tick with fewer queued sales than the batch size, all accepted,
// must not be stored as a learned limit (no 413 happened).
func TestSyncPush413_ShortAcceptedBatchLearnsNothing(t *testing.T) {
	r := newPushRig(t)
	r.seed(t, 3)
	noKicks(t)
	r.fake.limit.Store(1 << 30)
	r.tick()
	if got := r.setting("sync.push_batch_size"); got != "" {
		t.Fatalf("learned size = %q, want unset", got)
	}
	if r.queued(t) != 0 {
		t.Fatalf("queued = %d, want 0", r.queued(t))
	}
}

// Review O2: a 413 then a non-413 failure in the same tick still keeps the
// smaller size, so the next tick does not re-send the refused 50-sale body.
func TestSyncPush413_ThenOtherErrorKeepsLearnedSize(t *testing.T) {
	r := newPushRig(t)
	r.seed(t, 50)
	noKicks(t)
	var n atomic.Int32
	r.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	r.tick()
	if got := r.setting("sync.push_batch_size"); got != "25" {
		t.Fatalf("learned size = %q, want 25", got)
	}
	if r.setting("sync.push_cursor") != "" {
		t.Fatal("cursor moved without a 200")
	}
}

// AC7-AC11: one sale refused alone holds the queue behind it.
func TestSyncPush413_SingleOversizedSaleHoldsQueueAndSelfHeals(t *testing.T) {
	r := newPushRig(t)
	ctx := context.Background()
	t.Setenv("UT_AUTH", "off")
	initPagesI18n(t)
	noKicks(t)
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	seedLocalSale(t, r.replicaDp, "h-a", "R-h-a", base.Format(time.RFC3339))
	r.seedBig(t, "h-b", base.Add(time.Second), 20*r.oneSale)
	seedLocalSale(t, r.replicaDp, "h-c", "R-h-c", base.Add(2*time.Second).Format(time.RFC3339))
	seedLocalSale(t, r.replicaDp, "h-d", "R-h-d", base.Add(3*time.Second).Format(time.RFC3339))
	r.fake.limit.Store(int64(3 * r.oneSale))

	for i := 0; i < 3; i++ {
		r.tick()
	}
	if !r.onPrimary(t, "h-a") || r.onPrimary(t, "h-b") || r.onPrimary(t, "h-c") || r.onPrimary(t, "h-d") {
		t.Fatal("only h-a should be on the primary")
	}
	if want := base.Format(time.RFC3339) + "|h-a"; r.setting("sync.push_cursor") != want {
		t.Fatalf("cursor = %q, want %q", r.setting("sync.push_cursor"), want)
	}
	if q := r.queued(t); q != 3 {
		t.Fatalf("queued = %d, want 3", q)
	}
	if r.setting("sync.push_held_sale") != "h-b" {
		t.Fatalf("held = %q", r.setting("sync.push_held_sale"))
	}
	if n := len(recentLines("R-h-b")); n != 1 {
		t.Fatalf("problem lines naming R-h-b = %d, want 1: %+v", n, logging.Recent())
	}
	if !logging.HasOpenProblem(SalePushTooLargeProblemKey) {
		t.Fatal("problem should be open")
	}
	for _, id := range []string{"h-b", "h-c", "h-d"} {
		var n int
		if err := r.replicaDp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sale_lines WHERE sale_id=?`, id).Scan(&n); err != nil || n != 1 {
			t.Fatalf("local %s lines = %d err=%v", id, n, err)
		}
	}

	// Chip: warn + 3 queued.
	chip := func() string {
		mux := http.NewServeMux()
		registerSyncAdmin(mux, r.replicaDp)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/sync-chip", nil))
		return rec.Body.String()
	}
	for k, v := range map[string]string{"sync.till_id": "t", "sync.last_contact_at": time.Now().UTC().Format(time.RFC3339)} {
		if err := r.replicaDp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	body := chip()
	if !strings.Contains(body, "sync-chip warn") || !strings.Contains(body, "3") {
		t.Fatalf("held chip = %q", body)
	}

	// Checkout unaffected: a new sale joins the queue.
	seedLocalSale(t, r.replicaDp, "h-e", "R-h-e", base.Add(4*time.Second).Format(time.RFC3339))
	if q := r.queued(t); q != 4 {
		t.Fatalf("queued after new sale = %d", q)
	}

	// Self-heal.
	// The learned size is 1, so the backlog drains as it grows back (1, 2, ...).
	r.fake.limit.Store(1 << 30)
	for i := 0; i < 5 && r.queued(t) > 0; i++ {
		r.tick()
	}
	if q := r.queued(t); q != 0 {
		t.Fatalf("queued after heal = %d", q)
	}
	if r.setting("sync.push_held_sale") != "" {
		t.Fatalf("held not cleared: %q", r.setting("sync.push_held_sale"))
	}
	if logging.HasOpenProblem(SalePushTooLargeProblemKey) {
		t.Fatal("problem should be resolved")
	}
	if body := chip(); !strings.Contains(body, "sync-chip ok") {
		t.Fatalf("healed chip = %q", body)
	}
}
