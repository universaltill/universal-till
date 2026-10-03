package cloudsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
)

// ADR-0116 D6 (ut-docs#3524): a 401 carries a machine code; after 3
// consecutive 401s on a till-auth endpoint the till keeps selling offline,
// slows cloud calls to hourly and shows a status chip. A 503
// (auth_unavailable) never counts toward the 3, and a successful tick
// resets the streak.

func unauthorizedBody(code string) string {
	return fmt.Sprintf(`{"data":null,"error":{"code":%q,"message":"no"}}`, code)
}

// --- the machine code on the wire ---

func TestPost401DecodesMachineCode(t *testing.T) {
	for _, code := range []string{"device_revoked", "token_retired", "unauthorized"} {
		t.Run(code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(unauthorizedBody(code)))
			}))
			defer srv.Close()
			_, err := post(context.Background(), testCfg(srv.URL), "/v1/stores/sync", []byte("{}"))
			var se *statusError
			if !errors.As(err, &se) || se.StatusCode != http.StatusUnauthorized {
				t.Fatalf("err = %v, want a 401 statusError", err)
			}
			if se.Code != code {
				t.Fatalf("Code = %q, want %q", se.Code, code)
			}
			if want := "cloudsync: /v1/stores/sync returned 401"; se.Error() != want {
				t.Fatalf("Error() = %q, want the unchanged %q", se.Error(), want)
			}
		})
	}
}

// A 401 whose body is not the envelope (a proxy's HTML page, an older
// cloud) still fails the tick as a 401; it just carries no code.
func TestPost401WithoutEnvelopeHasNoCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("<html>401</html>"))
	}))
	defer srv.Close()
	_, err := post(context.Background(), testCfg(srv.URL), "/v1/stores/sync", []byte("{}"))
	var se *statusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusUnauthorized || se.Code != "" {
		t.Fatalf("err = %#v, want a 401 statusError with no code", err)
	}
}

// Additive only: a non-401 answer's body is not decoded into Code, so no
// existing caller's view of a 503/429/402 changes.
func TestPostNon401LeavesCodeEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"data":null,"error":{"code":"auth_unavailable","message":"x"}}`))
	}))
	defer srv.Close()
	_, err := post(context.Background(), testCfg(srv.URL), "/v1/stores/sync", []byte("{}"))
	var se *statusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusServiceUnavailable || se.Code != "" {
		t.Fatalf("err = %#v, want a 503 statusError with Code empty", err)
	}
}

func TestCheckin401DecodesMachineCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(unauthorizedBody("token_retired")))
	}))
	defer srv.Close()
	status, _, err := getCheckin(context.Background(), srv.URL, "store-1", "tok-1", false, 0)
	var se *statusError
	if status != http.StatusUnauthorized || !errors.As(err, &se) || se.Code != "token_retired" {
		t.Fatalf("status=%d err=%#v, want 401 with Code token_retired", status, err)
	}
}

func TestCheckin503LeavesCodeEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"data":null,"error":{"code":"auth_unavailable","message":"x"}}`))
	}))
	defer srv.Close()
	_, _, err := getCheckin(context.Background(), srv.URL, "store-1", "tok-1", false, 0)
	var se *statusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusServiceUnavailable || se.Code != "" {
		t.Fatalf("err = %#v, want a 503 statusError with Code empty", err)
	}
}

// --- the scheduler's 401 streak ---

func err401(code string) error {
	return &statusError{Path: "/v1/stores/sync", StatusCode: http.StatusUnauthorized, Code: code}
}

func err503() error {
	return &statusError{Path: "/v1/stores/sync", StatusCode: http.StatusServiceUnavailable}
}

// testScheduler is a scheduler with its own 401 tracker (never the
// package-level one the web layer reads) and a pinned random source.
func testScheduler(t *testing.T) *scheduler {
	t.Helper()
	origTick := tickIntervalNS.Load()
	t.Cleanup(func() { tickIntervalNS.Store(origTick) })
	tickIntervalNS.Store(int64(2 * time.Minute))
	return &scheduler{rng: func() float64 { return 0.999999 }, auth: &authTracker{}}
}

func TestSchedulerGoesHourlyOnTheThirdConsecutive401(t *testing.T) {
	s := testScheduler(t)
	d1 := s.next(err401("device_revoked"))
	d2 := s.next(err401("device_revoked"))
	if d1 >= time.Hour || d2 >= time.Hour {
		t.Fatalf("first two 401s: %v, %v — want ordinary backoff (below the 10m cap), not hourly yet", d1, d2)
	}
	if s.auth.status().Show {
		t.Fatal("chip shown after only 2 consecutive 401s")
	}
	if d3 := s.next(err401("device_revoked")); d3 != time.Hour {
		t.Fatalf("third consecutive 401: wait %v, want exactly 1h", d3)
	}
	// It stays hourly, never decaying back to the 10-minute cap.
	for i := 0; i < 5; i++ {
		if d := s.next(err401("device_revoked")); d != time.Hour {
			t.Fatalf("401 #%d: wait %v, want 1h", 4+i, d)
		}
	}
	if st := s.auth.status(); !st.Show || st.Code != "device_revoked" {
		t.Fatalf("status = %+v, want shown with device_revoked", st)
	}
}

func TestScheduler503NeverCountsTowardOrResetsThe401Streak(t *testing.T) {
	s := testScheduler(t)
	// 401, 503, 503, 401: only two 401s — no lockout.
	s.next(err401("unauthorized"))
	s.next(err503())
	s.next(err503())
	if d := s.next(err401("unauthorized")); d >= time.Hour {
		t.Fatalf("two 401s around 503s: wait %v, want ordinary backoff (503 must not count)", d)
	}
	if s.auth.status().Show {
		t.Fatal("503s counted toward the 3×401 rule")
	}
	// A 503 between them didn't reset the streak either: the next 401 is
	// the third.
	s.next(err503())
	if d := s.next(err401("unauthorized")); d != time.Hour {
		t.Fatalf("third 401 after a 503: wait %v, want 1h (503 must not reset)", d)
	}
	// Once locked out, a 503 (or a dead network) keeps the hourly pace and
	// the chip: only a success ends it.
	if d := s.next(err503()); d != time.Hour {
		t.Fatalf("503 while locked out: wait %v, want 1h", d)
	}
	if d := s.next(errors.New("dial tcp: no route to host")); d != time.Hour {
		t.Fatalf("transport error while locked out: wait %v, want 1h", d)
	}
	if !s.auth.status().Show {
		t.Fatal("a 503 cleared the chip")
	}
}

func TestSchedulerSuccessResetsThe401Streak(t *testing.T) {
	s := testScheduler(t)
	for i := 0; i < 3; i++ {
		s.next(err401("token_retired"))
	}
	if !s.auth.status().Show {
		t.Fatal("precondition: locked out after 3×401")
	}
	d := s.next(nil)
	lo := time.Duration(float64(2*time.Minute) * 0.8)
	hi := time.Duration(float64(2*time.Minute) * 1.2)
	if d < lo || d > hi {
		t.Fatalf("after a success: wait %v, want a normal jittered tick in [%v,%v]", d, lo, hi)
	}
	if s.auth.status().Show {
		t.Fatal("chip still shown after a successful tick")
	}
	// A success in the middle breaks the streak: 401, 401, ok, 401 is not
	// three in a row.
	s.next(err401("token_retired"))
	s.next(err401("token_retired"))
	s.next(nil)
	if d := s.next(err401("token_retired")); d >= time.Hour {
		t.Fatalf("401, 401, ok, 401: wait %v, want ordinary backoff", d)
	}
}

// Non-401 failures (429, 500, transport) keep exactly the generic backoff
// they had before this card.
func TestSchedulerNon401FailuresKeepTheGenericBackoff(t *testing.T) {
	s := testScheduler(t)
	for i := 0; i < 10; i++ {
		d := s.next(errors.New("boom"))
		if d > 10*time.Minute {
			t.Fatalf("transport failure #%d: wait %v, want capped at 10m", i+1, d)
		}
	}
	if s.auth.status().Show {
		t.Fatal("non-401 failures showed the credential chip")
	}
}

func TestAuthStatusNormalisesTheCode(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"device_revoked", "device_revoked"},
		{"token_retired", "token_retired"},
		{"unauthorized", "unauthorized"},
		{"", "unauthorized"},              // an older cloud, or a proxy's 401
		{"something_new", "unauthorized"}, // unknown: the generic case
	} {
		a := &authTracker{}
		for i := 0; i < 3; i++ {
			a.observe(err401(c.in))
		}
		if st := a.status(); !st.Show || st.Code != c.want {
			t.Errorf("code %q: status %+v, want shown with %q", c.in, st, c.want)
		}
	}
}

// The latest 401's code wins: a till first told "unauthorized" and then
// "device_revoked" shows the revoked chip.
func TestAuthStatusFollowsTheLatestCode(t *testing.T) {
	a := &authTracker{}
	a.observe(err401("unauthorized"))
	a.observe(err401("unauthorized"))
	a.observe(err401("device_revoked"))
	if st := a.status(); st.Code != "device_revoked" {
		t.Fatalf("status %+v, want device_revoked", st)
	}
}

// --- driven end to end: a revoked till keeps selling offline ---

// countingDead fails every request and counts it: "no cloud reachable".
type countingDead struct{ calls atomic.Int32 }

func (c *countingDead) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, errors.New("network unreachable (test)")
}

// A revoked till, driven through the real tick + the real scheduler + the
// package-level tracker the web layer reads: the third consecutive 401
// (a 503 auth_unavailable in between counts for nothing) slows the loop to
// hourly and raises AuthChipStatus — then a full sale completes with no
// network at all (ADR-0003). Sale setup follows
// internal/entitlement/sale_offline_test.go.
func TestRevokedTillLocksOutHourlyAndStillSellsOffline(t *testing.T) {
	fakeClock(t)
	resetTillAuth(t)

	var mu sync.Mutex
	answers := []int{http.StatusUnauthorized, http.StatusUnauthorized, http.StatusServiceUnavailable, http.StatusUnauthorized}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		code := answers[0]
		if len(answers) > 1 {
			answers = answers[1:]
		}
		mu.Unlock()
		w.WriteHeader(code)
		if code == http.StatusServiceUnavailable {
			_, _ = w.Write([]byte(`{"data":null,"error":{"code":"auth_unavailable","message":"x"}}`))
			return
		}
		_, _ = w.Write([]byte(unauthorizedBody("device_revoked")))
	}))
	defer srv.Close()

	origTick := tickIntervalNS.Load()
	t.Cleanup(func() { tickIntervalNS.Store(origTick) })
	tickIntervalNS.Store(int64(2 * time.Minute))
	sched := newScheduler()
	sched.rng = func() float64 { return 0.5 }

	cfg, settingsDB := testCfg(srv.URL), testDB(t)
	var waits []time.Duration
	for i := 0; i < 4; i++ {
		_, err := tick(context.Background(), cfg, settingsDB, Hooks{})
		if err == nil {
			t.Fatalf("tick %d succeeded against a revoking cloud", i+1)
		}
		waits = append(waits, sched.next(err))
		if i == 2 && AuthChipStatus().Show {
			t.Fatal("chip shown after 2×401 + 1×503: the 503 counted")
		}
	}
	if waits[3] != time.Hour {
		t.Fatalf("waits = %v, want the 4th (3rd 401) to be exactly 1h", waits)
	}
	if st := AuthChipStatus(); !st.Show || st.Code != "device_revoked" {
		t.Fatalf("AuthChipStatus = %+v, want shown with device_revoked", st)
	}

	// The sale: real migrated schema, real pos.CompleteSale, no network.
	dead := &countingDead{}
	prevDefault, prevClient := http.DefaultTransport, httpClient.Transport
	http.DefaultTransport, httpClient.Transport = dead, dead
	t.Cleanup(func() { http.DefaultTransport, httpClient.Transport = prevDefault, prevClient })

	d, err := db.Open(filepath.Join(t.TempDir(), "revoked.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, s := range []string{
		`INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Main')`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1', 'SKU1', 'Coffee Beans', 1000, 1)`,
		`INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at) VALUES ('inv1', 'itm1', NULL, 'loc1', 50, datetime('now'))`,
		`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('cash', 'Cash', 'cash', 1)`,
	} {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	saleID, err := pos.CompleteSale(context.Background(), d.DB, pos.SaleInput{
		SaleType:     "sale",
		Currency:     "EUR",
		TaxInclusive: true,
		Lines: []pos.SaleLineInput{{
			ItemID:             "itm1",
			Name:               "Coffee Beans",
			Qty:                2,
			UnitPrice:          money.FromMinor(1000),
			TaxRateBasisPoints: 1900,
			LocationID:         "loc1",
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2000)}},
	})
	if err != nil {
		t.Fatalf("CompleteSale on a revoked till with no network: %v", err)
	}
	var status string
	var total int64
	if err := d.DB.QueryRow(`SELECT status, total FROM sales WHERE id = ?`, saleID).Scan(&status, &total); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if status != "completed" || total != 2000 {
		t.Fatalf("sale = status %q total %d, want completed / 2000", status, total)
	}
	if n := dead.calls.Load(); n != 0 {
		t.Fatalf("sale attempted %d network call(s); checkout must be fully offline", n)
	}
	if !AuthChipStatus().Show {
		t.Fatal("the sale cleared the credential chip")
	}
}

// resetTillAuth clears the package-level tracker for one test and again
// afterwards, so no other test inherits a locked-out till.
func resetTillAuth(t *testing.T) {
	t.Helper()
	tillAuth.observe(nil) // a success clears the streak
	t.Cleanup(func() { tillAuth.observe(nil) })
}
