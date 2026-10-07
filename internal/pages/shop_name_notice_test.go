package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// --- GET /ui/shop-name-notice (ut-docs#3114): a legacy till still named
// "My Store" (or blank, or the cloud's "Universal Till store") shows a
// manager a dismissible "Name your shop" notice on every page — modelled
// 1:1 on GET /ui/pairing-notice (pending_pairings_test.go). ---

func shopNameNoticeMux(t *testing.T, storeName string) *http.ServeMux {
	t.Helper()
	t.Setenv("UT_AUTH", "on")
	_, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), common.KeyStoreName, storeName); err != nil {
		t.Fatalf("set store.name: %v", err)
	}
	mux := http.NewServeMux()
	registerShopNameNoticeUI(mux, d)
	return mux
}

func TestShopNameNoticeUI_ShownToManagerForPlaceholderName(t *testing.T) {
	for _, name := range []string{"My Store", "  my store ", "Universal Till store", ""} {
		t.Run(name, func(t *testing.T) {
			mux := shopNameNoticeMux(t, name)
			rec := getWithUser(mux, "/ui/shop-name-notice", &mgrUser)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, `id="shop-name-notice"`) {
				t.Fatalf("expected the notice element for placeholder %q, got: %s", name, body)
			}
			if !strings.Contains(body, `role="status"`) {
				t.Fatalf("expected a non-blocking role=status banner, got: %s", body)
			}
			if !strings.Contains(body, `href="/settings"`) {
				t.Fatalf("expected a link to /settings, got: %s", body)
			}
			if !strings.Contains(body, `data-action="dismiss-shop-name-notice"`) {
				t.Fatalf("expected the dismiss button, got: %s", body)
			}
			// Translated, not a raw key (initAuthTestI18n loads en.json).
			if !strings.Contains(body, "Name your shop") {
				t.Fatalf("expected the translated title, got: %s", body)
			}
			if strings.Contains(body, "<dialog") || strings.Contains(body, "showModal") {
				t.Fatalf("the notice must never be a modal (offline-first), got: %s", body)
			}
		})
	}
}

// TestShopNameNoticeUI_NeverShownOnSaleScreen (ut-docs#3114 review finding,
// real e2e regression): unlike the pairing/join notices this is modelled
// on — present only for the rare till mid-pairing — a placeholder store
// name is the common case, so this banner is on every poll. Mounted the
// same way on "/" as every other page, its height pushed the sale
// screen's basket/tile-grid/totals-row layouts (phone-sell-3059,
// portrait-tablet-sale-3050, tablet-tier-totals-416, phone-width-
// layout-413, sale-screen-213) out of the exact viewport height they
// budget for. The offline-first "never block the sale flow" rule means
// the sale screen gets none of this banner.
func TestShopNameNoticeUI_NeverShownOnSaleScreen(t *testing.T) {
	mux := shopNameNoticeMux(t, "My Store")
	rec := getWithUserFromPage(mux, "/ui/shop-name-notice", &mgrUser, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("the sale screen must never show the notice even for a placeholder name, got: %s", rec.Body.String())
	}
}

// getWithUserFromPage is getWithUser plus the HX-Current-URL header htmx
// sends on every poll, so a handler can tell which page mounted it.
func getWithUserFromPage(mux *http.ServeMux, path string, user *auth.User, currentURL string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("HX-Current-URL", "http://till.local"+currentURL)
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestShopNameNoticeUI_EmptyForRealName(t *testing.T) {
	mux := shopNameNoticeMux(t, "Corner Bakery")
	rec := getWithUser(mux, "/ui/shop-name-notice", &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected an empty body once the shop is named, got: %s", rec.Body.String())
	}
}

func TestShopNameNoticeUI_NeverShownToCashier(t *testing.T) {
	mux := shopNameNoticeMux(t, "My Store")
	rec := getWithUser(mux, "/ui/shop-name-notice", &cashUser)
	// Same "silent empty" convention as GET /ui/pairing-notice: a caller
	// without permission gets 200 + nothing, never a 403 on every page.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a cashier, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("a cashier must never see the notice, got: %s", rec.Body.String())
	}
}

func TestShopNameNoticeUI_EmptyWithoutSession(t *testing.T) {
	mux := shopNameNoticeMux(t, "My Store")
	rec := getWithUser(mux, "/ui/shop-name-notice", nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("expected 200 + empty body without a session, got %d: %s", rec.Code, rec.Body.String())
	}
}

// The base.html mount must keep polling after an empty response and must
// not share the partial's root id — same two bugs
// TestPairingNoticeMount_KeepsPollingAndUsesADistinctID locks in.
func TestShopNameNoticeMount_KeepsPollingAndUsesADistinctID(t *testing.T) {
	chdirRoot(t)
	b, err := os.ReadFile("web/ui/layouts/base.html")
	if err != nil {
		t.Fatalf("read base.html: %v", err)
	}
	body := string(b)
	i := strings.Index(body, `hx-get="/ui/shop-name-notice"`)
	if i < 0 {
		t.Fatalf("expected the shop-name-notice placeholder in base.html")
	}
	start := strings.LastIndex(body[:i], "<div")
	end := strings.Index(body[i:], ">")
	if start < 0 || end < 0 {
		t.Fatalf("could not isolate the placeholder <div> tag")
	}
	tag := body[start : i+end+1]
	if strings.Contains(tag, `hx-swap="outerHTML"`) {
		t.Fatalf("the placeholder must NOT use hx-swap=\"outerHTML\" (an empty poll would kill polling), got: %s", tag)
	}
	if !strings.Contains(tag, `hx-trigger="load, every 60s"`) {
		t.Fatalf("expected the placeholder to poll on load and every 60s, got: %s", tag)
	}
	if strings.Contains(tag, `id="shop-name-notice"`) {
		t.Fatalf("the placeholder must use a different id from the partial's root, got: %s", tag)
	}
}
