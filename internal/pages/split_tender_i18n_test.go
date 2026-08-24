package pages

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// ut-docs#925: the split-tender panel's status/validation copy is rendered by
// web/public/app.js, a shipped static file that guard-i18n.sh does not scan
// (CLAUDE.md, "Known gap"). Its strings therefore ride on data-msg-*
// attributes on #split-tender-card, the same bridge #barcode-scan-overlay
// uses. Before the fix these attributes did not exist at all and app.js
// hardcoded English literals, so a fa/RTL sale showed "Sale completed."
// amid otherwise-Persian UI.
//
// The tests below pin that bridge from both ends. NOTE ON TEST DESIGN
// (independent review finding, 2026-08-24): an earlier draft checked only
// that the template's attribute set and app.js's read set AGREED with each
// other. That is mutation-blind — deleting an attribute AND its read keeps
// the two sets consistent, so pasting `setStatus('Sale completed.')` back
// into app.js left the suite green. The expected attribute set is therefore
// pinned EXPLICITLY below, and the localization check asserts against the
// real locale file rather than merely "en differs from fa".

// wantSplitTenderMsgAttrs is the authoritative list of data-msg-* attributes
// the split-tender panel's JS depends on. Deleting a status string from
// app.js must fail this test, so this list is deliberately hardcoded and not
// derived from either the template or app.js.
var wantSplitTenderMsgAttrs = []string{
	"added",
	"already-covered",
	"amount-positive",
	"basket-unavailable",
	"change-exceeds",
	"change-note",
	"cleared",
	"filled",
	"need-payment",
	"network-error",
	"no-pending",
	"payment-failed",
	"removed",
	"sale-completed",
	"select-method",
	"submitting",
}

// splitTenderMsgLocaleKeys maps each attribute to the locale key it renders,
// so the tests can assert the rendered value really is that key's translation.
var splitTenderMsgLocaleKeys = map[string]string{
	"added":              "tender.status.added",
	"already-covered":    "tender.status.already_covered",
	"amount-positive":    "tender.status.amount_positive",
	"basket-unavailable": "tender.status.basket_unavailable",
	"change-exceeds":     "tender.status.change_exceeds",
	"change-note":        "tender.status.change_note",
	"cleared":            "tender.status.cleared",
	"filled":             "tender.status.filled",
	"need-payment":       "tender.status.need_payment",
	"network-error":      "tender.status.network_error",
	// The empty state reuses the key the server-rendered markup already uses.
	"no-pending":     "tender.no_pending",
	"payment-failed": "tender.status.payment_failed",
	"removed":        "tender.status.removed",
	"sale-completed": "tender.status.sale_completed",
	"select-method":  "tender.status.select_method",
	"submitting":     "tender.status.submitting",
}

// dataMsgAttrsOnSplitTenderCard returns the data-msg-* attributes declared on
// #split-tender-card in the rendered page.
func dataMsgAttrsOnSplitTenderCard(t *testing.T, page string) map[string]string {
	t.Helper()
	marker := strings.Index(page, `id="split-tender-card"`)
	if marker < 0 {
		t.Fatalf("#split-tender-card missing from rendered page")
	}
	tagStart := strings.LastIndex(page[:marker], "<div")
	if tagStart < 0 {
		t.Fatalf("could not find the opening tag of #split-tender-card")
	}
	// Walk to the '>' that closes this opening tag, skipping any '>' that
	// sits inside a quoted attribute value. A naive IndexByte('>') would
	// truncate the scan on e.g. x-show="n > 0" and silently mask real drift
	// (review nit, 2026-08-24).
	tagEnd := -1
	inQuote := byte(0)
	for i := tagStart; i < len(page); i++ {
		c := page[i]
		switch {
		case inQuote != 0:
			if c == inQuote {
				inQuote = 0
			}
		case c == '"' || c == '\'':
			inQuote = c
		case c == '>':
			tagEnd = i
		}
		if tagEnd >= 0 {
			break
		}
	}
	if tagEnd < 0 {
		t.Fatalf("unterminated #split-tender-card opening tag")
	}
	tag := page[tagStart:tagEnd]

	out := map[string]string{}
	re := regexp.MustCompile(`data-msg-([a-z-]+)="([^"]*)"`)
	for _, m := range re.FindAllStringSubmatch(tag, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// datasetKeysReadByAppJS returns the data-msg-* attribute names that
// initSplitTender() actually reads, derived from its `msg.msgXxx` accesses.
func datasetKeysReadByAppJS(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("web/public/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "function initSplitTender()")
	if start < 0 {
		t.Fatalf("initSplitTender() not found in app.js")
	}
	// Bound the scan to this function: its closing brace is the first one at
	// the function's own indentation (two spaces inside the file's IIFE).
	// The earlier anchor -- the `ready(initSplitTender)` call site -- sat
	// ~250 lines further down and swept up every sibling function in
	// between, so an unrelated panel added to the same IIFE produced a
	// confusing split-tender failure (review finding, 2026-08-24).
	rel := strings.Index(body[start:], "\n  }\n")
	if rel < 0 {
		t.Fatalf("could not find the end of initSplitTender() in app.js")
	}
	fn := body[start : start+rel]

	out := map[string]bool{}
	re := regexp.MustCompile(`\bmsg\.msg([A-Za-z0-9]+)\b`)
	for _, m := range re.FindAllStringSubmatch(fn, -1) {
		// msgNoPending -> no-pending
		var b strings.Builder
		for i, r := range m[1] {
			if r >= 'A' && r <= 'Z' {
				if i > 0 {
					b.WriteByte('-')
				}
				b.WriteRune(r + 32)
				continue
			}
			b.WriteRune(r)
		}
		out[b.String()] = true
	}
	return out
}

func loadLocale(t *testing.T, locale string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("web/locales/" + locale + ".json")
	if err != nil {
		t.Fatalf("read %s.json: %v", locale, err)
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse %s.json: %v", locale, err)
	}
	return out
}

func newSplitTenderI18nMux(t *testing.T) *http.ServeMux {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Test Item", Qty: 1, PriceCents: 100},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)
	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerIndex(mux, dp)
	return mux
}

func renderHome(t *testing.T, mux *http.ServeMux, query string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/"+query, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /%s = %d", query, rec.Code)
	}
	return rec.Body.String()
}

func TestSplitTenderStatusStringsRideOnDataMsgAttrs(t *testing.T) {
	mux := newSplitTenderI18nMux(t)
	attrs := dataMsgAttrsOnSplitTenderCard(t, renderHome(t, mux, ""))
	read := datasetKeysReadByAppJS(t)

	// 1. Every expected attribute is rendered by the template. Pinned to the
	//    hardcoded list on purpose: deleting an attribute must fail here even
	//    if its app.js read is deleted in the same edit.
	for _, name := range wantSplitTenderMsgAttrs {
		if _, ok := attrs[name]; !ok {
			t.Errorf("template no longer renders data-msg-%s on #split-tender-card", name)
		}
	}

	// 2. Every expected attribute is actually read by initSplitTender(). This
	//    is the half that catches a status string reverted to a literal.
	for _, name := range wantSplitTenderMsgAttrs {
		if !read[name] {
			t.Errorf("initSplitTender() no longer reads msg.msg%s — its status string is hardcoded again", kebabToCamel(name))
		}
	}

	// 3. No drift in either direction beyond the pinned set: an attribute
	//    app.js never reads is a dead locale key; a read with no attribute is
	//    a silently blank status line.
	want := map[string]bool{}
	for _, name := range wantSplitTenderMsgAttrs {
		want[name] = true
	}
	var unexpectedAttrs, unexpectedReads []string
	for name := range attrs {
		if !want[name] {
			unexpectedAttrs = append(unexpectedAttrs, "data-msg-"+name)
		}
	}
	for name := range read {
		if !want[name] {
			unexpectedReads = append(unexpectedReads, "msg.msg"+kebabToCamel(name))
		}
	}
	sort.Strings(unexpectedAttrs)
	sort.Strings(unexpectedReads)
	if len(unexpectedAttrs) > 0 {
		t.Errorf("template renders data-msg-* attributes not in wantSplitTenderMsgAttrs (add them there, or drop them): %v", unexpectedAttrs)
	}
	if len(unexpectedReads) > 0 {
		t.Errorf("initSplitTender() reads dataset keys not in wantSplitTenderMsgAttrs (status line would be blank): %v", unexpectedReads)
	}
}

func kebabToCamel(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		if r == '-' {
			up = true
			continue
		}
		if up && r >= 'a' && r <= 'z' {
			b.WriteRune(r - 32)
			up = false
			continue
		}
		up = false
		b.WriteRune(r)
	}
	return b.String()
}

func TestSplitTenderStatusStringsLocalize(t *testing.T) {
	mux := newSplitTenderI18nMux(t)

	for _, locale := range []string{"en", "fa", "ar", "tr"} {
		t.Run(locale, func(t *testing.T) {
			attrs := dataMsgAttrsOnSplitTenderCard(t, renderHome(t, mux, "?lang="+locale))
			want := loadLocale(t, locale)

			for _, name := range wantSplitTenderMsgAttrs {
				got, ok := attrs[name]
				if !ok {
					t.Errorf("data-msg-%s not rendered in %s", name, locale)
					continue
				}
				key := splitTenderMsgLocaleKeys[name]
				if want[key] == "" {
					t.Errorf("locale %s has no value for %q", locale, key)
					continue
				}
				// Positive assertion against the locale file itself: the
				// attribute must carry THIS locale's translation, not merely
				// "something different from English".
				if got != want[key] {
					t.Errorf("data-msg-%s in %s = %q, want %q (from %s)", name, locale, got, want[key], key)
				}
			}
		})
	}

	// The exact string this card was raised for: a completed sale in fa must
	// not render the English copy. Asserted on a value that is proven present
	// above, so a missing attribute can never make this vacuously pass.
	fa := dataMsgAttrsOnSplitTenderCard(t, renderHome(t, mux, "?lang=fa"))
	got, ok := fa["sale-completed"]
	if !ok {
		t.Fatalf("data-msg-sale-completed missing in fa — the ut-docs#925 defect is back")
	}
	if strings.Contains(got, "Sale completed") {
		t.Errorf("fa sale-completed still renders English copy: %q", got)
	}
}

// A translated string must take exactly as many %s placeholders as the
// English original. app.js's fmt() substitutes positionally, so a locale that
// drops one silently loses that value at runtime — e.g. a payment
// confirmation rendering the method but not the amount. Nothing else in CI
// checks this, and CLAUDE.md requires these keys to be mirrored by hand into
// the external ut-plugin-language-{de,es} packs (review finding, 2026-08-24).
func TestLocalePlaceholderCountsMatchEnglish(t *testing.T) {
	chdirRoot(t)
	en := loadLocale(t, "en")
	for _, locale := range []string{"fa", "ar", "tr"} {
		other := loadLocale(t, locale)
		for key, enVal := range en {
			got, ok := other[key]
			if !ok {
				t.Errorf("%s is missing key %q present in en.json", locale, key)
				continue
			}
			for _, verb := range []string{"%s", "%d"} {
				if a, b := strings.Count(enVal, verb), strings.Count(got, verb); a != b {
					t.Errorf("%s[%q] has %d %s placeholder(s), en has %d (%q vs %q)",
						locale, key, b, verb, a, got, enVal)
				}
			}
		}
	}
}

// app.js's fmt() understands only the plain `%s` verb. Any locale key the
// split-tender panel renders through it must therefore stay within that
// subset — `%d` or an indexed `%[1]s` (both legal on the Go side, and the
// natural reach for a translator reordering a sentence) would pass through
// literally onto the operator's screen.
func TestSplitTenderKeysUseOnlyPlainStringVerb(t *testing.T) {
	chdirRoot(t)
	indexed := regexp.MustCompile(`%\[\d+\]`)
	for _, locale := range []string{"en", "fa", "ar", "tr"} {
		values := loadLocale(t, locale)
		for _, key := range splitTenderMsgLocaleKeys {
			v := values[key]
			if strings.Contains(v, "%d") || indexed.MatchString(v) {
				t.Errorf("%s[%q] = %q uses a verb app.js's fmt() cannot render (only %%s is supported)",
					locale, key, v)
			}
			// Catch any other stray verb too.
			stray := regexp.MustCompile(`%[^s%]`)
			if m := stray.FindString(v); m != "" {
				t.Errorf("%s[%q] = %q contains unsupported verb %q", locale, key, v, fmt.Sprintf("%q", m))
			}
		}
	}
}
