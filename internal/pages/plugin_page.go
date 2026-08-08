package pages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Overridable in tests.
var pluginPagesDir = "./data/plugins"

// contentBundle is the localized plugin content contract: a plugin page ships
// content/<locale>.json files carrying categorized entries (the format the
// FAQ reference plugin publishes).
type contentBundle struct {
	Locale         string `json:"locale"`
	Version        string `json:"version"`
	RTL            bool   `json:"rtl"`
	FallbackLocale string `json:"fallback_locale"`
	Categories     []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		SortOrder int    `json:"sort_order"`
	} `json:"categories"`
	Entries []struct {
		ID          string   `json:"id"`
		Category    string   `json:"category"`
		Question    string   `json:"question"`
		Answer      string   `json:"answer"`
		SortOrder   int      `json:"sort_order"`
		LastUpdated string   `json:"last_updated"`
		Keywords    []string `json:"keywords"`
	} `json:"faq_entries"`
}

// registerPluginPages dispatches plugin-provided UI:
//   - GET /plugin/...              -> the page a 'page' entry registered at that route
//   - GET /ui/plugin-buttons       -> partial listing installed 'button' entries
//   - POST /api/plugins/entries/{plugin}/{key}/action -> publish the button's event
func registerPluginPages(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/plugin/", func(w http.ResponseWriter, r *http.Request) {
		entry, ok := findPageEntry(r, d)
		if !ok {
			http.NotFound(w, r)
			return
		}
		renderPluginPage(w, r, d, entry)
	})

	mux.HandleFunc("GET /ui/plugin-buttons", func(w http.ResponseWriter, r *http.Request) {
		buttons, _ := data.NewPluginRepo(d.Db).ListButtonEntries(r.Context())
		httpx.RenderPartial("ui/partials/plugin_buttons.html", map[string]any{
			"Buttons": buttons,
		})(w, r)
	})

	mux.HandleFunc("POST /api/plugins/entries/{plugin}/{key}/action", func(w http.ResponseWriter, r *http.Request) {
		pluginID, key := r.PathValue("plugin"), r.PathValue("key")
		buttons, err := data.NewPluginRepo(d.Db).ListButtonEntries(r.Context())
		if err != nil {
			http.Error(w, "failed to load plugin buttons", http.StatusInternalServerError)
			return
		}
		for _, b := range buttons {
			if b.PluginID != pluginID || b.EntryKey != key {
				continue
			}
			// The button's action is an event: hooks/processes subscribed to it
			// react; the publish is audited either way.
			eventType := b.TriggerEvent
			if eventType == "" {
				eventType = b.TargetAction
			}
			if eventType == "" {
				eventType = "plugin.button.pressed"
			}
			eventID, err := plugins.SharedBus(d.Db).Publish(r.Context(), eventType, map[string]any{
				"plugin_id": b.PluginID,
				"entry_key": b.EntryKey,
				"label":     b.Label,
			})
			if err != nil {
				http.Error(w, fmt.Sprintf("action failed: %v", err), http.StatusInternalServerError)
				return
			}
			// Envelope: { "data": …, "error": null } (universal-till/CLAUDE.md,
			// ut-docs#387: this endpoint used to respond bare).
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"data":  map[string]any{"success": true, "event": eventType, "event_id": eventID},
				"error": nil,
			})
			return
		}
		http.NotFound(w, r)
	})
}

// findPageEntry resolves the active plugin page entry registered at the
// request path.
func findPageEntry(r *http.Request, d *common.Deps) (data.PageEntryRow, bool) {
	entries, err := data.NewPluginRepo(d.Db).ListPageEntries(r.Context())
	if err != nil {
		return data.PageEntryRow{}, false
	}
	for _, e := range entries {
		if e.Route != "" && e.Route == r.URL.Path {
			return e, true
		}
	}
	return data.PageEntryRow{}, false
}

// renderPluginPage renders a plugin page inside the POS chrome. Content
// sources, in order: a localized content bundle (content/<locale>.json), a
// static content/index.html, else a plugin info card.
func renderPluginPage(w http.ResponseWriter, r *http.Request, d *common.Deps, entry data.PageEntryRow) {
	pluginDir := filepath.Join(pluginPagesDir, entry.PluginID, entry.PluginVersion)
	locale := httpx.ResolveLocale(w, r)
	base := map[string]any{
		// Entry labels are translator keys (plugins ship locales/ overlays);
		// plain-text labels pass through T unchanged.
		"title":     httpx.T(locale, entry.Label),
		"theme":     d.CurrentState().Theme,
		"menuItems": d.MenuSnapshot(),
	}

	if bundle, usedFallback, ok := loadContentBundle(filepath.Join(pluginDir, "content"), locale); ok {
		view := bundleView(bundle)
		view["FallbackNotice"] = usedFallback
		base["bundle"] = view
		httpx.Render("ui/pages/plugin_content.html", base)(w, r)
		return
	}

	if raw, err := os.ReadFile(filepath.Join(pluginDir, "content", "index.html")); err == nil {
		// The plugin bundle was Ed25519-verified at install; its page HTML is
		// trusted the same way its binary would be.
		base["pluginHTML"] = template.HTML(raw) //nolint:gosec
		httpx.Render("ui/pages/plugin_embed.html", base)(w, r)
		return
	}

	base["pluginHTML"] = template.HTML( //nolint:gosec
		"<p>" + template.HTMLEscapeString(entry.PluginName+" v"+entry.PluginVersion) + "</p>" +
			"<p class=\"empty\">This plugin registered a page but ships no page content.</p>")
	httpx.Render("ui/pages/plugin_embed.html", base)(w, r)
}

// baseLang returns the language subtag before the first '-' (lowercased),
// e.g. "en-GB" -> "en"; a tag with no '-' is returned lowercased as-is.
func baseLang(locale string) string {
	lower := strings.ToLower(locale)
	if i := strings.Index(lower, "-"); i >= 0 {
		return lower[:i]
	}
	return lower
}

// loadContentBundle picks the best content/<locale>.json for the POS locale:
// exact match, prefix match (en -> en-US/en-GB), same base language but a
// different region (en-GB requested, only en-US shipped), then en-US, then
// any bundle. (The bundle's own "fallback_locale" field is parsed but NOT
// consulted here — pre-existing, not addressed by this function.) The
// second return value reports whether
// the pick required a true language fallback (e.g. requested ja-JP but
// served en-US) as opposed to matching the requested language in some form
// (exact, regional prefix, or same base language) — the caller surfaces
// this to the page so staff know they're seeing a different language, not
// their own (spec FR-004 / US2 acceptance scenario 2). A same-base-language
// pick (en-GB -> en-US) is deliberately NOT a fallback for this purpose:
// it's still the requester's language, just a different region's bundle.
func loadContentBundle(contentDir, locale string) (bundle *contentBundle, usedFallback bool, ok bool) {
	files, err := os.ReadDir(contentDir)
	if err != nil {
		return nil, false, false
	}
	var names []string
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".json") {
			names = append(names, strings.TrimSuffix(f.Name(), ".json"))
		}
	}
	if len(names) == 0 {
		return nil, false, false
	}
	sort.Strings(names)

	pick := ""
	for _, n := range names {
		if strings.EqualFold(n, locale) {
			pick = n
			break
		}
	}
	if pick == "" {
		for _, n := range names {
			if strings.HasPrefix(strings.ToLower(n), strings.ToLower(locale)+"-") {
				pick = n
				break
			}
		}
	}
	if pick == "" {
		reqBase := baseLang(locale)
		for _, n := range names {
			if baseLang(n) == reqBase {
				pick = n
				break
			}
		}
	}
	matchedRequestedLanguage := pick != ""
	if pick == "" {
		for _, fallback := range []string{"en-US", names[0]} {
			for _, n := range names {
				if n == fallback {
					pick = n
					break
				}
			}
			if pick != "" {
				break
			}
		}
	}

	raw, err := os.ReadFile(filepath.Join(contentDir, pick+".json"))
	if err != nil {
		return nil, false, false
	}
	if !checksumValid(raw) {
		logging.L().Warnf("plugin content: %s failed checksum_sha256 verification (on-disk corruption?), refusing to render", filepath.Join(contentDir, pick+".json"))
		return nil, false, false
	}
	var b contentBundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, false, false
	}
	// A bundle without entries isn't a content page.
	if len(b.Entries) == 0 {
		return nil, false, false
	}
	return &b, !matchedRequestedLanguage, true
}

// checksumFieldPattern matches a populated checksum_sha256 field the same
// way ut-plugin-faq's scripts/checksum.py writes it.
var checksumFieldPattern = regexp.MustCompile(`(?i)("checksum_sha256":\s*")([0-9a-f]{64})(")`)

// checksumValid verifies a content bundle's self-referential checksum_sha256
// field, if present. The field can't hash the file's raw bytes as-is (that
// would be circular — it would need to already know its own value), so the
// checksum instead covers the file with its own checksum_sha256 VALUE
// zeroed out to a same-length placeholder (sha256 hex digests are always
// exactly 64 chars, so zeroing never changes the file's byte length or
// reflows anything). This is pure byte-level substitution, not JSON
// re-serialization, so it matches ut-plugin-faq's Python packaging tool
// exactly with no cross-language canonicalization risk.
//
// A bundle with no populated checksum field (older content, or a
// page-plugin author who hasn't adopted this convention) has nothing to
// verify and passes through unchanged — this is a defense-in-depth check
// against post-install on-disk corruption, not a replacement for the
// Ed25519 signature already verified at install time.
func checksumValid(raw []byte) bool {
	matches := checksumFieldPattern.FindAllSubmatch(raw, 2)
	switch len(matches) {
	case 0:
		return true
	case 1:
		want := strings.ToLower(string(matches[0][2]))
		zeroed := checksumFieldPattern.ReplaceAll(raw, []byte(`${1}`+strings.Repeat("0", 64)+`${3}`))
		sum := sha256.Sum256(zeroed)
		return hex.EncodeToString(sum[:]) == want
	default:
		// More than one checksum_sha256 field is itself a sign something is
		// wrong with the file (duplicate/injected key) — refuse rather than
		// guess which occurrence is authoritative.
		return false
	}
}

// bundleView groups bundle entries under their (sorted) categories for the
// template.
func bundleView(b *contentBundle) map[string]any {
	type entryView struct {
		Question, Answer string
		Sort             int
		// Search is a lowercased "question answer keyword1 keyword2..."
		// haystack for the client-side keyword filter (FR-005/SC-003) —
		// rendered as a data-search attribute, never displayed.
		Search string
	}
	type categoryView struct {
		Name    string
		Sort    int
		Entries []entryView
	}

	catName := map[string]string{}
	catSort := map[string]int{}
	for _, c := range b.Categories {
		catName[c.ID] = c.Name
		catSort[c.ID] = c.SortOrder
	}

	grouped := map[string][]entryView{}
	lastUpdated := ""
	for _, e := range b.Entries {
		haystack := strings.ToLower(strings.Join(append([]string{e.Question, e.Answer}, e.Keywords...), " "))
		grouped[e.Category] = append(grouped[e.Category], entryView{Question: e.Question, Answer: e.Answer, Sort: e.SortOrder, Search: haystack})
		// ISO 8601 (YYYY-MM-DD) dates compare lexicographically in
		// chronological order, so a plain string max is correct here.
		if e.LastUpdated > lastUpdated {
			lastUpdated = e.LastUpdated
		}
	}

	cats := make([]categoryView, 0, len(grouped))
	for id, entries := range grouped {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Sort < entries[j].Sort })
		name := catName[id]
		if name == "" {
			name = id
		}
		cats = append(cats, categoryView{Name: name, Sort: catSort[id], Entries: entries})
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Sort < cats[j].Sort })

	return map[string]any{
		"RTL":         b.RTL,
		"Categories":  cats,
		"Version":     b.Version,
		"LastUpdated": lastUpdated,
	}
}
