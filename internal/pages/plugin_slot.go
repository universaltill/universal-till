package pages

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
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
// at warn, never shown, never blocking the region or its page.
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
	"settings.sections": func(d *common.Deps, r *http.Request) bool { return canPerform(d, r, "settings") },
	// The Administration page (admin.html): its own gate (at least one
	// visible destination, registerAdmin) AND core reports (ADR-0149 §6).
	"admin.pages": func(d *common.Deps, r *http.Request) bool {
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
}

// pluginSlotPanelID is panel i's container id in slot.
func pluginSlotPanelID(slot string, i int) string {
	return fmt.Sprintf("plugin-slot-%s-%d", strings.ReplaceAll(slot, ".", "-"), i)
}

// contentSlotPanels asks the plugins filling slot and returns their
// prepared views in a stable (plugin id, entry key) order, failures
// dropped. Every ask runs in parallel with its own pluginSlotTimeout, so
// this returns within about one timeout whatever the plugins do.
func contentSlotPanels(ctx context.Context, d *common.Deps, slot, locale string, params map[string]string) []pluginSlotPanel {
	entries, err := data.NewPluginRepo(d.Db).ListPageEntries(ctx)
	if err != nil {
		logging.L().Warnf("plugin slot %s: list entries: %v", slot, err)
		return nil
	}
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

	p := make(map[string]string, len(params)+1)
	for k, v := range params {
		p[k] = v
	}
	p["slot"] = slot
	perm := "ui:slot:" + slot

	views := make([]*pluginview.View, len(fill))
	var wg sync.WaitGroup
	for i, e := range fill {
		wg.Add(1)
		go func() {
			defer logging.RecoverAndLog("pages.contentSlotPanels")
			defer wg.Done()
			raw, vctx, err := askPluginUIAs(ctx, d, e, perm, pluginSlotTimeout, pluginViewAskEvent, map[string]any{
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
				return
			}
			v := doc.Prepare(locale)
			if v.Title == "" {
				v.Title = httpx.T(locale, e.Label)
			}
			views[i] = &v
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
