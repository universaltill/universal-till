package pages

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pages/settingsnav"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pluginview"
)

// Plugin content slots (ADR-0121 §7, ut-docs#3872; format: ut-docs
// reference/plugin-views.md). A core-owned region asks every active plugin
// whose page entry declares the slot (manifest "slot", persisted as
// config_json "content_slot") and which holds ui:slot:<slot>, with
// ui.view.ask, in parallel, each bounded by pluginSlotTimeout. The answers
// are validated and drawn by core's own plugin view partials. A slow,
// broken, invalid, permission-less or silent plugin is skipped -- logged
// at warn, never shown, never blocking the region or its page. A
// permission-less plugin is never asked, and its denial is audited and
// warned once per process, not on every load (ut-docs#3945).
//
// Five regions load lazily from GET /ui/slot/{slot} (an htmx placeholder in
// the host page), gated by the host page's own permission. The setup
// wizard (auth-exempt, pre-operator) draws setup.wizard.steps inline and
// read-only instead; that slot has no route.

// pluginSlotTimeout bounds one plugin's answer in a slot (ADR-0121 §7:
// 2 s). A var so tests can shorten it.
var pluginSlotTimeout = 2 * time.Second

// pluginSlotMaxEntries caps how many entries one slot asks per render.
const pluginSlotMaxEntries = 8

// setupWizardSlot is drawn inline by the setup wizard (renderSetupSlot).
const setupWizardSlot = "setup.wizard.steps"

// settingsSectionsSlot and adminPagesSlot are drawn as entries of their
// host page's own switcher, one per plugin entry (ut-docs#3946).
const (
	settingsSectionsSlot = "settings.sections"
	adminPagesSlot       = "admin.pages"
)

// pluginSectionsCat is the Settings category plugin sections land in: a
// plugin group's id shape (settingsnav "g-<group>"), so it gets its own
// landing tile after the core categories.
const pluginSectionsCat = "g-plugins"

// pluginSlotPanelIDRe matches the id core gives a slot panel's container
// (pluginSlotPanelID); servePluginView answers an action into it only when
// HX-Target matches.
var pluginSlotPanelIDRe = regexp.MustCompile(`^plugin-slot-[a-z0-9-]{1,64}$`)

// pluginSlotGates is every lazily drawn slot and the host page's own
// permission, which also gates its route (fail closed).
var pluginSlotGates = map[string]func(d *common.Deps, r *http.Request) bool{
	// The catalog item-edit dialog (catalog.html).
	"item.edit.actions": func(d *common.Deps, r *http.Request) bool { return canPerform(d, r, "catalog_management") },
	// The Reports page (reports.html).
	"reports.panels": func(d *common.Deps, r *http.Request) bool { return canPerform(d, r, "reports") },
	// The End of day tab of Reports (reports_tab_eod.html).
	"eod.footer": func(d *common.Deps, r *http.Request) bool { return canPerform(d, r, "eod_report") },
	// The Settings page (settings.html).
	settingsSectionsSlot: func(d *common.Deps, r *http.Request) bool { return canPerform(d, r, "settings") },
	// The Administration page (admin.html): its own gate (at least one
	// visible destination, registerAdmin) AND core reports (ADR-0149 §6).
	adminPagesSlot: func(d *common.Deps, r *http.Request) bool {
		return canPerform(d, r, "reports") && len(visibleAdminEntries(d, r)) > 0
	},
}

var pluginSlotFiles = []string{
	filepath.Join("web", "ui", "partials", "pluginview", "slot.html"),
	filepath.Join("web", "ui", "partials", "pluginview", "document.html"),
	filepath.Join("web", "ui", "partials", "pluginview", "data.html"),
	filepath.Join("web", "ui", "partials", "pluginview", "controls.html"),
	filepath.Join("web", "ui", "partials", "pluginview", "poll.html"),
}

// pluginSlotPanel is one plugin's answer, ready for pluginview_slot.
type pluginSlotPanel struct {
	ID   string // the container id actions target (pluginSlotPanelIDRe)
	Body pluginViewBody
}

// pluginSlotView is what pluginview_slot renders.
type pluginSlotView struct {
	Slot   string
	Panels []pluginSlotPanel
	// Section: one entry's panel drawn inside its own switcher section
	// (ut-docs#3946), which is already the card.
	Section bool
}

// pluginSlotPanelID is panel i's container id in slot.
func pluginSlotPanelID(slot string, i int) string {
	return fmt.Sprintf("plugin-slot-%s-%d", strings.ReplaceAll(slot, ".", "-"), i)
}

// contentSlotEntries lists the entries that fill slot for this render, in
// the stable (plugin id, entry key) order, capped at pluginSlotMaxEntries.
// Nothing is asked: a /plugin/ view entry whose plugin holds
// ui:slot:<slot> qualifies. The Settings and Admin switchers list these
// (ut-docs#3946), and contentSlotPanels asks them.
func contentSlotEntries(ctx context.Context, d *common.Deps, slot string) []data.PageEntryRow {
	entries, err := data.NewPluginRepo(d.Db).ListPageEntries(ctx)
	if err != nil {
		logging.L().Warnf("plugin slot %s: list entries: %v", slot, err)
		return nil
	}
	perm := "ui:slot:" + slot
	var fill []data.PageEntryRow
	for _, e := range entries {
		if e.Slot != slot || e.View == "" {
			continue
		}
		// Actions post to the entry's own route, which serves a view only
		// under /plugin/ (servePluginEntry) and never a reserved prefix.
		if !strings.HasPrefix(e.Route, "/plugin/") {
			continue
		}
		if _, reserved := plugins.ReservedPageRoutePrefix(e.Route); reserved {
			continue
		}
		// Checked here, not at ask time, so a revoked entry never takes one
		// of the pluginSlotMaxEntries places.
		granted, first, err := plugins.CheckPermissionAuditOnce(ctx, d.Db, e.PluginID, perm)
		if err != nil {
			logging.L().Warnf("plugin slot %s: %s %q skipped: %v", slot, e.PluginID, e.View, err)
			continue
		}
		if !granted {
			if first {
				logging.L().Warnf("plugin slot %s: %s %q skipped: %s not granted", slot, e.PluginID, e.View, perm)
			}
			continue
		}
		fill = append(fill, e)
	}
	sort.SliceStable(fill, func(i, j int) bool {
		if fill[i].PluginID != fill[j].PluginID {
			return fill[i].PluginID < fill[j].PluginID
		}
		return fill[i].EntryKey < fill[j].EntryKey
	})
	if len(fill) > pluginSlotMaxEntries {
		logging.L().Warnf("plugin slot %s: %d entries, drawing the first %d", slot, len(fill), pluginSlotMaxEntries)
		fill = fill[:pluginSlotMaxEntries]
	}
	return fill
}

// askSlotEntry asks one slot entry for its view, bounded by
// pluginSlotTimeout; nil when it is slow, broken, invalid or silent
// (logged at warn). A view with no title of its own takes the entry's
// label.
func askSlotEntry(ctx context.Context, d *common.Deps, slot, locale string, params map[string]string, e data.PageEntryRow) *pluginview.View {
	p := make(map[string]string, len(params)+1)
	for k, v := range params {
		p[k] = v
	}
	p["slot"] = slot
	raw, vctx, err := askPluginUIAs(ctx, d, e, "ui:slot:"+slot, pluginSlotTimeout, pluginViewAskEvent, map[string]any{
		"view":   e.View,
		"params": p,
		"locale": locale,
	})
	var doc *pluginview.Document
	if err == nil {
		doc, err = pluginview.DecodeViewAnswer(raw, vctx)
	}
	if err != nil {
		logging.L().Warnf("plugin slot %s: %s %q skipped: %v", slot, e.PluginID, e.View, err)
		return nil
	}
	v := doc.Prepare(locale)
	if v.Title == "" {
		v.Title = httpx.T(locale, e.Label)
	}
	return &v
}

// contentSlotPanels asks the plugins filling slot and returns their
// prepared views in a stable (plugin id, entry key) order, failures
// dropped. Every ask runs in parallel with its own pluginSlotTimeout, so
// this returns within about one timeout whatever the plugins do.
func contentSlotPanels(ctx context.Context, d *common.Deps, slot, locale string, params map[string]string) []pluginSlotPanel {
	fill := contentSlotEntries(ctx, d, slot)
	views := make([]*pluginview.View, len(fill))
	var wg sync.WaitGroup
	for i, e := range fill {
		wg.Add(1)
		go func() {
			defer logging.RecoverAndLog("pages.contentSlotPanels")
			defer wg.Done()
			views[i] = askSlotEntry(ctx, d, slot, locale, params, e)
		}()
	}
	wg.Wait()

	var out []pluginSlotPanel
	for i, v := range views {
		if v == nil {
			continue
		}
		id := pluginSlotPanelID(slot, len(out))
		out = append(out, pluginSlotPanel{ID: id, Body: pluginViewBody{Route: fill[i].Route, View: *v, Target: id}})
	}
	return out
}

// pluginSlotSection is one plugin entry of a switcher-hosted slot
// (settings.sections, ut-docs#3946): its own section in the host page's
// switcher, whose panel loads from URL.
type pluginSlotSection struct {
	ID    string // the section's element id, also its #fragment
	Label string // the entry's label, localized
	URL   string // GET /ui/slot/{slot}/{plugin}/{entry}
}

var pluginSectionIDChars = regexp.MustCompile(`[^a-z0-9]+`)

// pluginSectionNavRows is the Settings sidebar's rows for sections, under
// one Plugins heading.
func pluginSectionNavRows(locale string, sections []pluginSlotSection) []settingsnav.Row {
	rows := make([]settingsnav.Row, 0, len(sections))
	for _, s := range sections {
		rows = append(rows, settingsnav.Row{Key: s.ID, Label: s.Label, Group: httpx.T(locale, "nav.plugins"), Cat: pluginSectionsCat})
	}
	return rows
}

// pluginSlotSections lists slot's entries as switcher sections for r, or
// nil when r fails the slot's host gate.
func pluginSlotSections(r *http.Request, d *common.Deps, slot, locale string) []pluginSlotSection {
	gate, ok := pluginSlotGates[slot]
	if !ok || !gate(d, r) {
		return nil
	}
	var out []pluginSlotSection
	seen := map[string]int{}
	for _, e := range contentSlotEntries(r.Context(), d, slot) {
		id := "plugin-section-" + strings.Trim(pluginSectionIDChars.ReplaceAllString(strings.ToLower(e.PluginID+"-"+e.EntryKey), "-"), "-")
		// "a-b"/"c" and "a"/"b-c" fold to the same id; suffix the later
		// one so each section keeps its own card and switcher row.
		if seen[id]++; seen[id] > 1 {
			id += "-" + strconv.Itoa(seen[id])
		}
		out = append(out, pluginSlotSection{
			ID:    id,
			Label: httpx.T(locale, e.Label),
			URL:   "/ui/slot/" + url.PathEscape(slot) + "/" + url.PathEscape(e.PluginID) + "/" + url.PathEscape(e.EntryKey),
		})
	}
	return out
}

// pluginEntrySlotAllowed reports whether r may open a page entry that
// declares slot: an entry with no slot is allowed; otherwise the slot's
// host-page gate decides, failing closed for setup.wizard.steps and unknown
// slots (ut-docs#3973). The Plugins page's Docs button uses it too, so the
// button never disagrees with the route (ut-docs#3994).
func pluginEntrySlotAllowed(d *common.Deps, r *http.Request, slot string) bool {
	if slot == "" {
		return true
	}
	gate, ok := pluginSlotGates[slot]
	return ok && gate(d, r)
}

// registerPluginSlots serves GET /ui/slot/{slot}: the panels of one lazily
// drawn slot as an HTML fragment (empty when no plugin answers). Unknown
// slots and setup.wizard.steps are 404; a viewer without the host page's
// permission gets 403 and no content.
func registerPluginSlots(mux *http.ServeMux, d *common.Deps) {
	// A slot a manifest may declare but no screen draws would silently
	// never render; say so at startup (a new slot needs a host).
	for _, s := range plugins.ContentSlots() {
		if _, ok := pluginSlotGates[s]; !ok && s != setupWizardSlot {
			logging.L().Errorf("plugin content slot %q has no host screen", s)
		}
	}
	mux.HandleFunc("GET /ui/slot/{slot}", func(w http.ResponseWriter, r *http.Request) {
		slot := r.PathValue("slot")
		gate, ok := pluginSlotGates[slot]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !gate(d, r) {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		panels := contentSlotPanels(r.Context(), d, slot, locale, pluginViewParams(r))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if len(panels) == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderWith(pluginSlotFiles, httpx.FuncsFor(locale))("pluginview_slot", pluginSlotView{Slot: slot, Panels: panels})(w, r)
	})
	// GET /ui/slot/{slot}/{plugin}/{entry} (ut-docs#3946): one entry's
	// panel, for its own section in the host page's switcher
	// (pluginSlotSections). Same gate as the whole slot; an entry that does
	// not fill the slot for this render is 404. A plugin that does not
	// answer gets the unavailable notice under its label, so the section
	// is never blank. The panel keeps its index in the whole slot as its
	// id, so sections on one page never share one.
	mux.HandleFunc("GET /ui/slot/{slot}/{plugin}/{entry}", func(w http.ResponseWriter, r *http.Request) {
		slot := r.PathValue("slot")
		gate, ok := pluginSlotGates[slot]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !gate(d, r) {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		plugin, key := r.PathValue("plugin"), r.PathValue("entry")
		for i, e := range contentSlotEntries(r.Context(), d, slot) {
			if e.PluginID != plugin || e.EntryKey != key {
				continue
			}
			locale := httpx.ResolveLocale(w, r)
			id := pluginSlotPanelID(slot, i)
			body := pluginViewBody{Route: e.Route, Target: id}
			if v := askSlotEntry(r.Context(), d, slot, locale, pluginViewParams(r), e); v != nil {
				body.View = *v
			} else {
				body.View.Title = httpx.T(locale, e.Label)
				body.Unavailable = true
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			httpx.RenderWith(pluginSlotFiles, httpx.FuncsFor(locale))("pluginview_slot", pluginSlotView{
				Slot: slot, Panels: []pluginSlotPanel{{ID: id, Body: body}}, Section: true,
			})(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

// renderSetupSlot draws setup.wizard.steps for the setup wizard, inline
// and read-only (no actions: the wizard is auth-exempt and the entries'
// /plugin/ routes are not). "" when no plugin answers.
func renderSetupSlot(r *http.Request, d *common.Deps, locale string) template.HTML {
	panels := contentSlotPanels(r.Context(), d, setupWizardSlot, locale, nil)
	if len(panels) == 0 {
		return ""
	}
	for i := range panels {
		panels[i].Body.ReadOnly = true
	}
	rec := httptest.NewRecorder()
	httpx.RenderWith(pluginSlotFiles, httpx.FuncsFor(locale))("pluginview_slot", pluginSlotView{Slot: setupWizardSlot, Panels: panels})(rec, r)
	if rec.Code != http.StatusOK {
		logging.L().Warnf("plugin slot %s: render failed (%d)", setupWizardSlot, rec.Code)
		return ""
	}
	return template.HTML(rec.Body.String()) //nolint:gosec // core's own html/template output: every plugin value in it was escaped
}
