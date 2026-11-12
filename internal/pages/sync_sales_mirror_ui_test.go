package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Tills page's sales-copy card (ut-docs#5074 M5).

func getCard(t *testing.T, mux http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/tills/sales-mirror", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("card: %d %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func TestSalesMirrorCard_MainTillSwitchAndTillLines(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	shop := newMirrorShop(t, "A", "B")
	mux := http.NewServeMux()
	registerSalesMirrorUI(mux, shop.main.d)
	registerSyncSalesMirror(mux, shop.main.d)
	body := getCard(t, mux)
	for _, want := range []string{"Copy of every sale on each till", ">Switched off<", "/api/sync/sales-mirror/enable", "Turn copying on"} {
		if !strings.Contains(body, want) {
			t.Fatalf("off card lacks %q:\n%s", want, body)
		}
	}
	// Turning it on from the card answers the card itself, now on.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/sales-mirror/enable", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Turn copying off") {
		t.Fatalf("enable from the card: %d %s", rec.Code, rec.Body)
	}
	for range 3 {
		shop.sell(shop.main, 100)
	}
	shop.setShop("sales_mirror.enabled", "1")
	epoch, _, _ := shop.main.d.Settings.Get(t.Context(), "sales_mirror.epoch")
	shop.setShop("sales_mirror.epoch", epoch)
	shop.feedAfter(shop.tills["A"], epoch, 1, 10)
	body = getCard(t, mux)
	for _, want := range []string{">Switched on<", "Copies are kept for 400 days.", "Till A: 2 changes to go", "Till B: not copied yet"} {
		if !strings.Contains(body, want) {
			t.Fatalf("on card lacks %q:\n%s", want, body)
		}
	}
}

func TestSalesMirrorCard_AdditionalTillShowsItsCopy(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	shop := newMirrorShop(t, "B")
	b := shop.tills["B"]
	mux := http.NewServeMux()
	registerSalesMirrorUI(mux, b.d)
	salesMirrorPullerRef.Lock()
	salesMirrorPullerRef.p = shop.puller(b)
	salesMirrorPullerRef.Unlock()
	t.Cleanup(func() { salesMirrorPullerRef.Lock(); salesMirrorPullerRef.p = nil; salesMirrorPullerRef.Unlock() })
	if body := getCard(t, mux); !strings.Contains(body, ">Switched off for the shop<") || strings.Contains(body, "/enable") {
		t.Fatalf("off on an additional till:\n%s", body)
	}
	shop.enable()
	shop.sell(shop.main, 100)
	shop.pull(b)
	shop.pull(b)
	body := getCard(t, mux)
	for _, want := range []string{"Up to date", "Sales copied: 1, since"} {
		if !strings.Contains(body, want) {
			t.Fatalf("additional till card lacks %q:\n%s", want, body)
		}
	}
}

// Every state the puller reports has words (M5 review finding 10).
func TestSalesMirrorCard_EveryStateHasAKey(t *testing.T) {
	for _, st := range []string{"off", "killed", "on", "paused:low_disk", "paused:budget", "paused:version_wait", "needs_tls", "main_off", "satellite"} {
		if salesMirrorStateKeys[st] == "" {
			t.Errorf("state %q has no words", st)
		}
	}
}

// Mid catch-up the card says "Catching up" with the count, never "Up to
// date" (finding 2); a kill switch on the main till shows, never "Turn copying on"
// (finding 3); satellites are not listed.
func TestSalesMirrorCard_CatchingUpKilledAndSatellite(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	shop := newMirrorShop(t, "B", "S")
	b := shop.tills["B"]
	if _, err := shop.main.d.Db.Exec(`UPDATE tills SET role = 'satellite' WHERE id = ?`, shop.tills["S"].tillID); err != nil {
		t.Fatal(err)
	}
	shop.enable()
	for range 150 {
		shop.sell(shop.main, 100)
	}
	p := shop.puller(b)
	salesMirrorPullerRef.Lock()
	salesMirrorPullerRef.p = p
	salesMirrorPullerRef.Unlock()
	t.Cleanup(func() { salesMirrorPullerRef.Lock(); salesMirrorPullerRef.p = nil; salesMirrorPullerRef.Unlock() })
	shop.pull(b) // state re-sync of all 150
	shop.sell(shop.main, 100)
	// One feed page only: stop after it by cutting the transport.
	orig := p.client
	p.client = &http.Client{Transport: &failAfter{n: 0, rt: orig.Transport}}
	shop.pull(b)
	p.client = orig
	p.setStatus(func(s *SalesMirrorStatus) { s.HeadSeq += 5 })
	bm := http.NewServeMux()
	registerSalesMirrorUI(bm, b.d)
	if body := getCard(t, bm); !strings.Contains(body, "Catching up") || strings.Contains(body, "Up to date") {
		t.Fatalf("mid catch-up card:\n%s", body)
	}
	mm := http.NewServeMux()
	registerSalesMirrorUI(mm, shop.main.d)
	if body := getCard(t, mm); strings.Contains(body, "Till S") {
		t.Fatalf("a satellite is listed:\n%s", body)
	}
	_ = shop.main.d.Settings.Set(t.Context(), "sync.sales_mirror_off", "1")
	body := getCard(t, mm)
	if !strings.Contains(body, "Off on this till") || strings.Contains(body, "Turn copying on") || strings.Contains(body, "Turn copying off") {
		t.Fatalf("killed main till card:\n%s", body)
	}
}

// A refused press from the card answers the card with the reason (htmx
// swaps only a 2xx); the card GET is manager-only.
func TestSalesMirrorCard_RefusalsShowInPlace(t *testing.T) {
	shop := newMirrorShop(t, "B")
	mux := http.NewServeMux()
	registerSalesMirrorUI(mux, shop.main.d)
	registerSyncSalesMirror(mux, shop.main.d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/tills/sales-mirror", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("card without a manager session: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/sales-mirror/enable", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✗") {
		t.Fatalf("refused press: %d %s", rec.Code, rec.Body)
	}
	if v, _, _ := shop.main.d.Settings.Get(t.Context(), "sales_mirror.enabled"); v == "1" {
		t.Fatal("a refused press switched the copy on")
	}
}
