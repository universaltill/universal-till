package pages

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
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
}

type mirrorShop struct {
	t      *testing.T
	main   *mirrorTill
	mainID string // M's own LAN till id (discovery.TillID)
	server *httptest.Server
	client *http.Client
	tills  map[string]*mirrorTill
}

func newMirrorShop(t *testing.T, additional ...string) *mirrorShop {
	t.Helper()
	mux, dp := newSyncSalesTestDeps(t)
	shop := &mirrorShop{t: t, main: &mirrorTill{name: "M", d: dp, mux: mux}, tills: map[string]*mirrorTill{}}
	registerMirrorMainRoutes(mux, dp)
	shop.server = httptest.NewTLSServer(mux)
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
// (registerSyncSales, already on the mux): grows with M2.
func registerMirrorMainRoutes(_ *http.ServeMux, _ *common.Deps) {}

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
