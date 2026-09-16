package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// stripCSSComments removes every /* ... */ block from src, returning the
// CSS a browser would actually apply. An UNTERMINATED block (a `/*` with no
// closing `*/` before EOF) swallows the rest of the file, exactly as a real
// CSS parser does -- that is the whole point: a rule that only *appears* in
// the file but sits inside a comment must not read as present.
func stripCSSComments(src string) string {
	var b strings.Builder
	for {
		i := strings.Index(src, "/*")
		if i < 0 {
			b.WriteString(src)
			return b.String()
		}
		b.WriteString(src[:i])
		rest := src[i+2:]
		j := strings.Index(rest, "*/")
		if j < 0 {
			// Unterminated: everything from here on is comment.
			return b.String()
		}
		src = rest[j+2:]
	}
}

func appCSSForTest(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	registerStatic(mux)
	registerThemes(mux, &common.Deps{})
	req := httptest.NewRequest(http.MethodGet, "/public/app.css", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /public/app.css = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// TestOrderTypePromptStage_AtPayCSSRuleIsLiveNotCommentedOut is a
// regression pin for the review finding on ut-docs#2282: the at_pay
// toggle-hiding rules shipped after a comment block whose terminator had
// been typed as the HTML `-->` instead of the CSS `*/`, so the rules were
// swallowed by that comment and never applied -- at_pay mode still showed
// the basket-top dine-in/takeaway toggle it is defined to hide. Nothing in
// the Go build, the linters, any existing test or any CI guard can see
// that, and a plain strings.Contains(appCSS, ...) check would have PASSED
// on the broken file, because the rule text really was there -- inside a
// comment. So this asserts against the COMMENT-STRIPPED stylesheet.
func TestOrderTypePromptStage_AtPayCSSRuleIsLiveNotCommentedOut(t *testing.T) {
	live := stripCSSComments(appCSSForTest(t))
	for _, want := range []string{
		`body[data-order-type-prompt-stage="at_pay"] .order-type-toggle-group`,
		`body[data-order-type-prompt-stage="at_pay"] .order-type-mixed`,
	} {
		if !strings.Contains(live, want) {
			t.Errorf("app.css: selector %q is not live -- it is missing, or it is inside a CSS comment (check the comment above it closes with */ and not -->)", want)
		}
	}
	// #table-picker is a SIBLING of the toggle group inside .order-type-row
	// and must stay reachable in at_pay mode (ADR-0054 table assignment is
	// unrelated to this setting) -- so the row itself is never hidden.
	if strings.Contains(live, `body[data-order-type-prompt-stage="at_pay"] .order-type-row {`) {
		t.Errorf("app.css: at_pay must not hide the whole .order-type-row -- #table-picker lives there too")
	}
}

// TestAppCSS_NoUnterminatedCommentBlock is the general half of the same
// finding: an unterminated /* ... block silently disables every rule after
// it until the next stray */, which is invisible to every other gate in
// this repo. Cheap to check once, here.
func TestAppCSS_NoUnterminatedCommentBlock(t *testing.T) {
	src := appCSSForTest(t)
	rest := src
	for {
		i := strings.Index(rest, "/*")
		if i < 0 {
			return
		}
		rest = rest[i+2:]
		j := strings.Index(rest, "*/")
		if j < 0 {
			line := 1 + strings.Count(src[:len(src)-len(rest)-2], "\n")
			t.Fatalf("app.css: unterminated CSS comment opened at line %d -- every rule after it is dead", line)
		}
		rest = rest[j+2:]
	}
}
