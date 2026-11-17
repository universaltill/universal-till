package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ut-docs#6400 (Microsoft Store policy 11.13): every listing's page carries a
// "Report this listing" link to the marketplace's report page, and the store
// pages link the marketplace publisher terms and content guidelines. The links
// are plain anchors to the website: nothing is fetched while rendering.

func TestStoreReportHrefPointsAtTheMarketplaceReportPage(t *testing.T) {
	cases := []struct{ base, id, want string }{
		{"https://cloud.example.test", "7c1e2a90-4d3b", "https://cloud.example.test/ui/en-gb/plugins/7c1e2a90-4d3b/report"},
		{"https://cloud.example.test/", "with space/slash", "https://cloud.example.test/ui/en-gb/plugins/with%20space%2Fslash/report"},
		{"", "abc", "https://cloud.universaltill.com/ui/en-gb/plugins/abc/report"},
	}
	for _, c := range cases {
		if got := storeReportHref(c.base, c.id); got != c.want {
			t.Errorf("storeReportHref(%q,%q) = %q, want %q", c.base, c.id, got, c.want)
		}
	}
}

func TestStoreDetailPageHasReportLinkAndMarketplaceTerms(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux := newStoreDetailMux(t,
		marketplace.PluginSummary{ID: "7c1e2a90-4d3b", Name: "Card reader", Version: "2.4.0", CanonicalType: "payment", Vendor: "Acme"},
		marketplace.PluginSummary{ID: "free-one", Name: "Dark theme", Version: "1.0", CanonicalType: "theme", TrustTier: "official"},
	)
	for _, id := range []string{"7c1e2a90-4d3b", "free-one"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins/store/"+id, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: GET detail = %d", id, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			`data-testid="store-report-link"`, `/ui/en-gb/plugins/` + id + `/report"`, "Report this listing",
			`href="https://www.universaltill.com/en-gb/legal/marketplace-terms/"`,
			`href="https://www.universaltill.com/en-gb/legal/marketplace-content-guidelines/"`,
			"Marketplace publisher terms", "Marketplace content guidelines",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: detail page missing %q", id, want)
			}
		}
		if !strings.Contains(body, `rel="noopener"`) {
			t.Errorf("%s: external links need rel=noopener", id)
		}
	}
}

func TestStoreGridFooterLinksMarketplaceTerms(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux := newStoreDetailMux(t, marketplace.PluginSummary{ID: "a", Name: "A", Version: "1", CanonicalType: "theme"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins/store", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET store = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`href="https://www.universaltill.com/en-gb/legal/marketplace-terms/"`,
		`href="https://www.universaltill.com/en-gb/legal/marketplace-content-guidelines/"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("store grid missing %q", want)
		}
	}
	if strings.Contains(body, "store-report-link") {
		t.Error("the report link belongs on a listing's own page")
	}
}
