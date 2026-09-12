package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#2183: before this card, #pos-alert was declared directly inside
// only three pages' own content blocks (index.html, admin.html, items.html)
// — every other page rendered through base.html (e.g. /pin here) had no
// #pos-alert element at all, so app.js's htmx:responseError/sendError
// listeners (showAlert(), ut-docs#213) silently no-op'd there:
// document.getElementById('pos-alert') returned null. base.html now
// declares it once, itself, for every page — this pins that a page which
// never included it directly (pin.html) gets exactly one #pos-alert
// element via the real production Render() call site (auth_page.go's
// GET /pin), not a bespoke test-only file set.
func TestPosAlert_PresentOnPageThatNeverIncludedItDirectly(t *testing.T) {
	InitI18n(realI18n(t), "en")
	h := Render("ui/pages/pin.html", map[string]any{
		"title":     "Change PIN",
		"theme":     "",
		"menuItems": nil,
		"errKey":    "",
	})
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/pin", nil))
	if w.Code != 200 {
		t.Fatalf("Render pin.html = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if got := strings.Count(body, `id="pos-alert"`); got != 1 {
		t.Fatalf("pin.html (never included #pos-alert directly) rendered through base.html: want exactly 1 #pos-alert element, got %d\n%.2000s", got, body)
	}
	if !strings.Contains(body, `data-msg-server="`) || !strings.Contains(body, `data-msg-network="`) {
		t.Fatalf("#pos-alert rendered without app.js's expected data-msg-server/data-msg-network attributes, got: %.2000s", body)
	}
}
