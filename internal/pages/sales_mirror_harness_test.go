package pages

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/diskspace"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// The multi-till harness for the sales copy (ut-docs#5074, design §13 "M8").
// It builds one shop: a main till M serving its real sync mux over TLS
// (httptest), and additional tills (A, B, C …), each a real migrated
// database with the replica identity a join leaves (sync.primary_url,
// sync.bearer, sync.till_id, sync.receipt_prefix, sync.primary_till_id),
// enrolled on M's real tills roster. Sales are rung with the real
// pos.CompleteSale and travel to M through the real journal push
// (syncPushTick → POST /api/sync/sales → applyJournal). It grows with each
// task: M1 proves the change log, M2/M3 the feed and the copy.

type mirrorTill struct {
	name   string
	d      *common.Deps
	mux    *http.ServeMux
	tillID string // the id M's roster knows it by ('' for M itself)
	bearer string
	sold   int
	puller *salesMirrorPuller
}

type mirrorShop struct {
	t      *testing.T
	main   *mirrorTill
	mainID string // M's own LAN till id (discovery.TillID)
	server *httptest.Server
	client *http.Client
	tills  map[string]*mirrorTill
	feed   *salesMirrorServer
	// copyRequests counts every request to the sales-copy routes on M.
	copyRequests atomic.Int64
}

func newMirrorShop(t *testing.T, additional ...string) *mirrorShop {
	t.Helper()
	mux, dp := newSyncSalesTestDeps(t)
	shop := &mirrorShop{t: t, main: &mirrorTill{name: "M", d: dp, mux: mux}, tills: map[string]*mirrorTill{}}
	shop.feed = registerMirrorMainRoutes(mux, dp)
	registerSyncPeerCloses(mux, dp)
	shop.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/sync/sales-mirror") {
			shop.copyRequests.Add(1)
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(shop.server.Close)
	shop.client = shop.server.Client()
	id, err := discovery.TillID(t.Context(), dp.Settings)
	if err != nil {
		t.Fatal(err)
	}
	shop.mainID = id
	for i, name := range additional {
		shop.addTill(name, i+2)
	}
	return shop
}

// registerMirrorMainRoutes adds the main till's routes beyond the journal
// (registerSyncSales, already on the mux): the sales-copy feed (M2).
func registerMirrorMainRoutes(mux *http.ServeMux, d *common.Deps) *salesMirrorServer {
	return registerSyncSalesMirror(mux, d)
}

func (s *mirrorShop) addTill(name string, tillNo int) *mirrorTill {
	t := s.t
	t.Helper()
	ctx := t.Context()
	bearer := fmt.Sprintf("bearer-%s-%d", name, tillNo)
	id, err := data.NewTillsRepo(s.main.d.Db).InsertTill(ctx, "Till "+name, hashBearer(bearer))
	if err != nil {
		t.Fatal(err)
	}
	mux, dp := newSyncSalesTestDeps(t)
	for k, v := range map[string]string{
		"sync.primary_url":                s.server.URL,
		"sync.bearer":                     bearer,
		"sync.till_id":                    id,
		"sync.receipt_prefix":             fmt.Sprintf("T%d-", tillNo),
		discovery.PrimaryTillIDSettingKey: s.mainID,
	} {
		if err := dp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	till := &mirrorTill{name: name, d: dp, mux: mux, tillID: id, bearer: bearer}
	s.tills[name] = till
	return till
}

// setShop sets a shop-wide setting on every till, as the admin bundle would.
func (s *mirrorShop) setShop(k, v string) {
	s.t.Helper()
	for _, till := range append([]*mirrorTill{s.main}, s.values()...) {
		if err := till.d.Settings.Set(s.t.Context(), k, v); err != nil {
			s.t.Fatal(err)
		}
	}
}

func (s *mirrorShop) values() []*mirrorTill {
	var out []*mirrorTill
	for _, n := range []string{"A", "B", "C", "D"} {
		if v, ok := s.tills[n]; ok {
			out = append(out, v)
		}
	}
	return out
}

// sell rings one real sale on till (the main till when till is s.main).
func (s *mirrorShop) sell(till *mirrorTill, amount int64) string {
	s.t.Helper()
	id, err := pos.CompleteSale(context.Background(), till.d.Db, pos.SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: true,
		Lines: []pos.SaleLineInput{{ItemID: "itm1", Name: "Apple", Qty: 1, UnitPrice: money.FromMinor(amount),
			TaxRateBasisPoints: 2000, LocationID: "loc_main"}},
		Payments:               []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(amount)}},
		AllowNegativeInventory: true,
	})
	if err != nil {
		s.t.Fatalf("sell on %s: %v", till.name, err)
	}
	if till.tillID != "" {
		// The journal push's cursor is created_at at one-second precision
		// and it sends 50 at a time, so a batch boundary inside one second
		// would skip the rest of that second (a pre-existing journal limit,
		// reported separately; real tills never ring 50 sales a second).
		// Give each harness sale on an additional till its own second.
		till.sold++
		at := time.Now().UTC().Add(-2 * time.Hour).Add(time.Duration(till.sold) * time.Second).Format(time.RFC3339)
		if _, err := till.d.Db.Exec(`UPDATE sales SET created_at = ?, completed_at = ? WHERE id = ?`, at, at, id); err != nil {
			s.t.Fatal(err)
		}
	}
	return id
}

// push runs one real journal push from till to M.
func (s *mirrorShop) push(till *mirrorTill) {
	s.t.Helper()
	syncPushTick(s.t.Context(), till.d, s.client)
}

func harnessCount(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// M1: with the shop setting on, the main till logs its own sales and every
// sale an additional till journals to it (origin = that till), in the same
// transaction as the apply; the additional tills log nothing. Off, nothing
// is logged anywhere.
func TestSalesMirrorHarness_M1_LogCoversJournaledAndOwnSales(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", on), func(t *testing.T) {
			shop := newMirrorShop(t, "A", "B")
			if on {
				shop.setShop(data.SalesMirrorEnabledKey, "1")
			}
			a, b := shop.tills["A"], shop.tills["B"]
			saleA := shop.sell(a, 250)
			saleB := shop.sell(b, 300)
			saleM := shop.sell(shop.main, 400)
			shop.push(a)
			shop.push(b)

			mdb := shop.main.d.Db
			if n := harnessCount(t, mdb, `SELECT COUNT(*) FROM sales WHERE id IN (?, ?, ?)`, saleA, saleB, saleM); n != 3 {
				t.Fatalf("main till holds %d of the 3 sales", n)
			}
			for _, till := range shop.values() {
				if n := harnessCount(t, till.d.Db, `SELECT COUNT(*) FROM sales_mirror_log`); n != 0 {
					t.Errorf("additional till %s logged %d rows", till.name, n)
				}
			}
			logged := harnessCount(t, mdb, `SELECT COUNT(*) FROM sales_mirror_log`)
			if !on {
				if logged != 0 {
					t.Fatalf("main till logged %d rows with the setting off", logged)
				}
				return
			}
			// The latest pointer per sale names its origin: A, B, and '' (M).
			for sale, origin := range map[string]string{saleA: a.tillID, saleB: b.tillID, saleM: ""} {
				var got string
				if err := mdb.QueryRow(`SELECT origin_till_id FROM sales_mirror_log WHERE ref_id = ? ORDER BY seq DESC LIMIT 1`, sale).Scan(&got); err != nil {
					t.Fatalf("no log row for %s: %v", sale, err)
				}
				if got != origin {
					t.Errorf("sale %s logged with origin %q, want %q", sale, got, origin)
				}
				// S0b (#2168): a journaled sale is one INSERT with its final
				// provenance, and the no-op provenance stamp after it changes
				// no logged column, so each sale is logged exactly once.
				if n := harnessCount(t, mdb, `SELECT COUNT(*) FROM sales_mirror_log WHERE ref_id = ?`, sale); n != 1 {
					t.Errorf("sale %s logged %d times, want once", sale, n)
				}
			}
		})
	}
}

// --- M3: the additional tills' pull ---

// puller returns till's sales-copy puller, wired to the shop's TLS client
// (the harness trusts the test server's certificate in place of a pin),
// no page pause, and a roomy disk.
func (s *mirrorShop) puller(till *mirrorTill) *salesMirrorPuller {
	if till.puller == nil {
		p := newSalesMirrorPuller(till.d, s.client)
		p.pinned = func(context.Context) bool { return true }
		p.pause = 0
		p.probe = func(string) (diskspace.Usage, error) { return diskspace.Usage{Total: 100 << 30, Free: 50 << 30}, nil }
		till.puller = p
	}
	return till.puller
}

// pull runs one real pull tick on till.
func (s *mirrorShop) pull(till *mirrorTill) {
	s.t.Helper()
	s.puller(till).tick(s.t.Context())
}

type copyRow struct {
	status, receipt, origin string
	total                   int64
	lines, payments         int
}

func copyRows(t *testing.T, db *sql.DB, table, linesTable, payTable, originExpr string) map[string]copyRow {
	t.Helper()
	rows, err := db.Query(`SELECT s.id, s.status, s.receipt_no, ` + originExpr + `, s.total,
  (SELECT COUNT(*) FROM ` + linesTable + ` l WHERE l.sale_id = s.id),
  (SELECT COUNT(*) FROM ` + payTable + ` p WHERE p.sale_id = s.id)
FROM ` + table + ` s WHERE s.status IN ('completed', 'voided')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]copyRow{}
	for rows.Next() {
		var id string
		var r copyRow
		if err := rows.Scan(&id, &r.status, &r.receipt, &r.origin, &r.total, &r.lines, &r.payments); err != nil {
			t.Fatal(err)
		}
		out[id] = r
	}
	return out
}

// assertCopyEqualsMain checks till's copy holds exactly the main till's
// current state of every sale but till's own: same status, receipt, origin,
// total and children; nothing extra (no duplicates, no own sales).
func (s *mirrorShop) assertCopyEqualsMain(till *mirrorTill) {
	t := s.t
	t.Helper()
	want := copyRows(t, s.main.d.Db, "sales", "sale_lines", "payments", "CASE WHEN s.till_id = '' THEN '"+s.mainID+"' ELSE s.till_id END")
	got := copyRows(t, till.d.Db, "mirror_sales", "mirror_sale_lines", "mirror_payments", "s.origin_till_id")
	for id, w := range want {
		if w.origin == till.tillID {
			if _, ok := got[id]; ok {
				t.Errorf("%s holds a copy of its own sale %s", till.name, id)
			}
			continue
		}
		g, ok := got[id]
		if !ok {
			t.Errorf("%s's copy is missing sale %s (%+v)", till.name, id, w)
			continue
		}
		if g != w {
			t.Errorf("%s's copy of %s = %+v, main till has %+v", till.name, id, g, w)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("%s's copy holds %s, which the main till does not", till.name, id)
		}
	}
}

func (s *mirrorShop) seqAcked(till *mirrorTill) int64 {
	var n int64
	_ = s.main.d.Db.QueryRow(`SELECT COALESCE(MAX(acked_seq), -1) FROM sales_mirror_acks WHERE till_id = ?`, till.tillID).Scan(&n)
	return n
}

// pushCloses runs the journal tick's closes step from till (the harness
// trusts the test server's certificate in place of a pin).
func (s *mirrorShop) pushCloses(till *mirrorTill) {
	s.t.Helper()
	syncPushCloses(s.t.Context(), till.d, s.client, s.server.URL, till.bearer, func(context.Context) bool { return true })
}
