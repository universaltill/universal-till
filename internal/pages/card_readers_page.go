package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/universaltill/universal-till/internal/cardreaders"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6566: the card reader picker (every signed-in operator, a cashier
// included: read + choose only) and the manager's reader management page
// (Settings > Payments > Card readers, `settings` action, cashier gets 403).

var methodKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// readerPickerRow is one reader in the picker.
type readerPickerRow struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Method       string `json:"method"`
	ProviderName string `json:"-"` // payment_methods.name (a translator key)
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Reason       string `json:"reason"` // i18n key, "" when ready
	Selectable   bool   `json:"selectable"`
	Default      bool   `json:"default"`
	LastUsed     bool   `json:"last_used"` // a payment was taken on this reader (the "Last used" tag needs it)
}

// readerPickerView is what the picker fragment and its JSON twin carry.
type readerPickerView struct {
	Method     string            `json:"method"`
	Rows       []readerPickerRow `json:"readers"`
	DefaultID  string            `json:"default"`
	Pinned     bool              `json:"pinned"`
	Auto       bool              `json:"auto"` // exactly one selectable reader, nothing pinned: no picker
	Selectable int               `json:"selectable"`
	Total      money.Money       `json:"-"`
}

func reasonKeyFor(s cardreaders.State, plugin string) string {
	if plugin != "" {
		return "tap.reason." + plugin
	}
	switch s {
	case cardreaders.Busy:
		return "tap.reason.reader_busy"
	case cardreaders.Offline:
		return "tap.reason.reader_unreachable"
	case cardreaders.NeedsSetup:
		return "tap.reason.reader_needs_setup"
	case cardreaders.Unknown, cardreaders.Unchecked:
		return "card_reader.reason.unknown"
	}
	return ""
}

// buildReaderPicker assembles the picker for method ("" = every managed
// method). The provider state is asked only here (never on a sell-screen
// render), cached briefly and bounded; core's own busy/pinned knowledge wins.
func buildReaderPicker(ctx context.Context, d *common.Deps, method string) (readerPickerView, error) {
	view := readerPickerView{Method: method}
	repo := data.NewCardReaderRepo(d.Db)
	all, err := repo.ListActive(ctx)
	if err != nil {
		return view, err
	}
	names := map[string]string{}
	if pms, err := data.NewPOSRepo(d.Db).ListActivePaymentMethods(ctx); err == nil {
		for _, m := range pms {
			names[m.ID] = m.Name
		}
	}
	managed := map[string]bool{}
	var rows []data.CardReader
	for _, c := range all {
		if method != "" && c.MethodKey != method {
			continue
		}
		ok, seen := managed[c.MethodKey]
		if !seen {
			ok, err = data.NewPluginRepo(d.Db).HasActiveHook(ctx, c.PluginID, readersHook(c.MethodKey))
			if err != nil {
				return view, err
			}
			managed[c.MethodKey] = ok
		}
		if ok && readerEligible(c, readerSourcePOS) {
			rows = append(rows, c)
		}
	}
	if d.Engine != nil {
		view.Total = d.Engine.Basket().Total
	}

	// A tender locked on an unknown outcome is pinned to its reader.
	pinnedID := ""
	unknownRows := map[string]bool{}
	if d.Engine != nil && d.Engine.TenderLock() != "" {
		for _, u := range d.Engine.PendingTender().Unknown {
			if u.ReaderRowID == "" {
				continue
			}
			unknownRows[u.ReaderRowID] = true
			if method == "" || u.MethodID == method {
				pinnedID = u.ReaderRowID
			}
		}
	}
	if pinnedID != "" {
		if c, err := repo.Get(ctx, pinnedID); err == nil {
			rows = []data.CardReader{c}
			view.Pinned = true
		}
	}

	inflight := cardreaders.SharedInFlight()
	var probes []cardreaders.Probe
	if !view.Pinned {
		for _, c := range rows {
			if !inflight.Has(c.ID) && !unknownRows[c.ID] {
				probes = append(probes, cardreaders.Probe{ID: c.ID, Method: c.MethodKey, Ref: c.ReaderRef})
			}
		}
	}
	states := map[string]cardreaders.Answer{}
	if len(probes) > 0 {
		states = cardreaders.SharedCache().States(ctx, probes, readerStateQuerier(d))
	}

	bestAt := ""
	for _, c := range rows {
		a := states[c.ID]
		if a.State == "" {
			a.State = cardreaders.Unknown
		}
		switch {
		case view.Pinned:
			a = cardreaders.Answer{State: cardreaders.Unknown}
		case inflight.Has(c.ID) || unknownRows[c.ID]:
			a = cardreaders.Answer{State: cardreaders.Busy, Reason: "reader_busy"}
		}
		row := readerPickerRow{
			ID: c.ID, Name: c.Name, Method: c.MethodKey, ProviderName: names[c.MethodKey], Kind: c.Kind,
			State: string(a.State), Reason: reasonKeyFor(a.State, a.Reason), Selectable: a.State.Selectable(),
			LastUsed: c.LastUsedAt != "",
		}
		if row.State == string(cardreaders.Ready) {
			row.Reason = ""
		}
		if row.Selectable {
			view.Selectable++
			// default: the newest last-used selectable reader, else the first.
			if view.DefaultID == "" || c.LastUsedAt > bestAt {
				view.DefaultID, bestAt = c.ID, c.LastUsedAt
			}
		}
		view.Rows = append(view.Rows, row)
	}
	for i := range view.Rows {
		view.Rows[i].Default = view.Rows[i].ID == view.DefaultID
	}
	view.Auto = !view.Pinned && view.Selectable == 1
	return view, nil
}

func registerCardReaders(mux *http.ServeMux, d *common.Deps) {
	// The picker: any signed-in operator (a sale-only cashier picks a reader).
	mux.HandleFunc("GET /api/pos/card-readers", func(w http.ResponseWriter, r *http.Request) {
		method := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("method")))
		if method != "" && !methodKeyRe.MatchString(method) {
			http.Error(w, "invalid method", http.StatusBadRequest)
			return
		}
		// Only the cashier's till has a picker; the self-order kiosk never does.
		if s := r.URL.Query().Get("source"); s != "" && s != readerSourcePOS {
			http.Error(w, "no reader picker for this surface", http.StatusForbidden)
			return
		}
		view, err := buildReaderPicker(r.Context(), d, method)
		if err != nil {
			logging.L().Errorf("card readers: picker: %v", err)
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pos.error.server", "card-readers", err)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			rows := view.Rows
			if rows == nil {
				rows = []readerPickerRow{}
			}
			view.Rows = rows
			_ = json.NewEncoder(w).Encode(map[string]any{"data": view, "error": nil})
			return
		}
		httpx.RenderPartial("ui/partials/card_reader_picker.html", view)(w, r)
	})

	registerCardReaderSettings(mux, d)
}

// ---- manager page ----

type readerSettingsRow struct {
	data.CardReader
	ProviderName string // translator key
	KindKey      string
	NeedsUpdate  bool   // plugin lacks the readers hook: listed read-only
	LegacyKey    string // plugin setting that would be cleared on remove ("" = none)
	PluginName   string
}

type readerProviderOption struct{ Key, Name string }

type readerSettingsView struct {
	Rows         []readerSettingsRow
	Providers    []readerProviderOption
	Count, Max   int
	AtLimit      bool
	NeedsUpdate  []readerProviderOption
	Msg, MsgKind string
	MsgArg       string
	Restored     bool
}

func buildReaderSettings(ctx context.Context, d *common.Deps) (readerSettingsView, error) {
	view := readerSettingsView{Max: data.MaxActiveCardReaders}
	repo := data.NewCardReaderRepo(d.Db)
	plug := data.NewPluginRepo(d.Db)
	rows, err := repo.ListActive(ctx)
	if err != nil {
		return view, err
	}
	entries, err := plug.ListPaymentEntries(ctx)
	if err != nil {
		return view, err
	}
	hook := map[string]bool{}
	label := map[string]string{}
	pluginName := map[string]string{}
	for _, e := range entries {
		ok, err := plug.HasActiveHook(ctx, e.PluginID, readersHook(e.EntryKey))
		if err != nil {
			return view, err
		}
		hook[e.EntryKey] = ok
		label[e.EntryKey] = e.Label
		pluginName[e.EntryKey] = e.PluginName
		if ok {
			view.Providers = append(view.Providers, readerProviderOption{Key: e.EntryKey, Name: e.Label})
		}
	}
	seenUpdate := map[string]bool{}
	for _, c := range rows {
		row := readerSettingsRow{CardReader: c, ProviderName: label[c.MethodKey], KindKey: "card_reader.kind." + c.Kind, PluginName: pluginName[c.MethodKey]}
		if !hook[c.MethodKey] {
			row.NeedsUpdate = true
			if !seenUpdate[c.MethodKey] {
				seenUpdate[c.MethodKey] = true
				view.NeedsUpdate = append(view.NeedsUpdate, readerProviderOption{Key: c.MethodKey, Name: label[c.MethodKey]})
			}
		}
		if k, err := repo.LegacySettingKey(ctx, c); err == nil {
			row.LegacyKey = k
		}
		view.Rows = append(view.Rows, row)
	}
	view.Count = len(rows)
	view.AtLimit = view.Count >= data.MaxActiveCardReaders
	return view, nil
}

// settingsDenied is the cashier's 403 on a manager reader route.
func readerSettingsDenied(d *common.Deps, w http.ResponseWriter, r *http.Request) bool {
	if canPerform(d, r, "settings") {
		return false
	}
	httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
	return true
}

func readerAudit(d *common.Deps, r *http.Request, id, action string, payload map[string]any) {
	settingsAudit(r, data.NewPOSRepo(d.Db), elevationCheck{Outcome: allowed, ActorID: settingsActorID(r)}, "card_reader", id, action, payload)
}

func registerCardReaderSettings(mux *http.ServeMux, d *common.Deps) {
	renderBody := func(w http.ResponseWriter, r *http.Request, status int, msgKey, msgKind, arg string) {
		view, err := buildReaderSettings(r.Context(), d)
		if err != nil {
			logging.L().Errorf("card readers: settings: %v", err)
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "common.error.server", "card-readers", err)
			return
		}
		view.Msg, view.MsgKind, view.MsgArg = msgKey, msgKind, arg
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		httpx.RenderPartial("ui/partials/card_reader_settings_body.html", view)(w, r)
	}
	fail := func(w http.ResponseWriter, r *http.Request, err error) {
		key, status := "common.error.server", http.StatusInternalServerError
		arg := ""
		switch {
		case errors.Is(err, data.ErrCardReaderName):
			key, status = "card_reader.settings.err_name", http.StatusUnprocessableEntity
		case errors.Is(err, data.ErrCardReaderRef), errors.Is(err, data.ErrCardReaderKind):
			key, status = "card_reader.settings.err_ref", http.StatusUnprocessableEntity
		case errors.Is(err, data.ErrCardReaderLimit):
			key, status, arg = "card_reader.settings.limit_reached", http.StatusUnprocessableEntity, fmt.Sprint(data.MaxActiveCardReaders)
		case errors.Is(err, data.ErrCardReaderNotFound):
			key, status = "card_reader.invalid", http.StatusNotFound
		default:
			logging.L().Errorf("card readers: %v", err)
		}
		renderBody(w, r, status, key, "error", arg)
	}

	mux.HandleFunc("GET /settings/card-readers", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "settings") {
			return
		}
		view, err := buildReaderSettings(r.Context(), d)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		httpx.Render("ui/pages/card_readers.html", map[string]any{
			"title":     httpx.T(locale, "page.title.card_readers"),
			"theme":     d.ResolvedTheme(r),
			"menuItems": d.MenuSnapshot(),
			"readers":   view,
		})(w, r)
	})

	mux.HandleFunc("POST /settings/card-readers", func(w http.ResponseWriter, r *http.Request) {
		if readerSettingsDenied(d, w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderBody(w, r, http.StatusBadRequest, "card_reader.settings.err_ref", "error", "")
			return
		}
		method := strings.ToLower(strings.TrimSpace(r.Form.Get("method_key")))
		view, err := buildReaderSettings(r.Context(), d)
		if err != nil {
			fail(w, r, err)
			return
		}
		pluginID := ""
		if entries, err := data.NewPluginRepo(d.Db).ListPaymentEntries(r.Context()); err == nil {
			for _, e := range entries {
				if e.EntryKey == method {
					pluginID = e.PluginID
				}
			}
		}
		qualified := false
		for _, p := range view.Providers {
			if p.Key == method {
				qualified = true
			}
		}
		if !qualified || pluginID == "" {
			// A plugin without the readers hook can't honour reader_id.
			renderBody(w, r, http.StatusUnprocessableEntity, "card_reader.settings.err_ref", "error", "")
			return
		}
		c, err := data.NewCardReaderRepo(d.Db).Create(r.Context(), data.CardReader{
			Name: r.Form.Get("name"), MethodKey: method, PluginID: pluginID, ReaderRef: strings.TrimSpace(r.Form.Get("reader_ref")),
		})
		if err != nil {
			fail(w, r, err)
			return
		}
		cardreaders.SharedCache().Reset()
		readerAudit(d, r, c.ID, "card_reader_added", map[string]any{"method": method, "name": c.Name, "kind": c.Kind})
		renderBody(w, r, http.StatusOK, "card_reader.settings.saved", "ok", "")
	})

	mux.HandleFunc("POST /settings/card-readers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if readerSettingsDenied(d, w, r) {
			return
		}
		id := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			renderBody(w, r, http.StatusBadRequest, "card_reader.settings.err_name", "error", "")
			return
		}
		repo := data.NewCardReaderRepo(d.Db)
		var err error
		switch {
		case r.Form.Has("name"):
			err = repo.Rename(r.Context(), id, r.Form.Get("name"))
		case r.Form.Get("move") == "up":
			err = repo.Move(r.Context(), id, -1)
		case r.Form.Get("move") == "down":
			err = repo.Move(r.Context(), id, 1)
		case r.Form.Has("unattended"):
			err = repo.SetUnattendedDefault(r.Context(), id, r.Form.Get("unattended") == "1")
		default:
			renderBody(w, r, http.StatusBadRequest, "card_reader.settings.err_name", "error", "")
			return
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		readerAudit(d, r, id, "card_reader_changed", map[string]any{})
		renderBody(w, r, http.StatusOK, "card_reader.settings.saved", "ok", "")
	})

	mux.HandleFunc("POST /settings/card-readers/{id}/remove", func(w http.ResponseWriter, r *http.Request) {
		if readerSettingsDenied(d, w, r) {
			return
		}
		id := r.PathValue("id")
		// A payment running on the reader, or a locked (outcome unknown)
		// tender pinned to it, still needs it: the retry on that same reader
		// is how the till learns whether the card was charged (review, #6566).
		if readerNeededByAPayment(d, id) {
			renderBody(w, r, http.StatusConflict, "card_reader.settings.err_in_use", "error", "")
			return
		}
		repo := data.NewCardReaderRepo(d.Db).OnSettingsChanged(func() { bumpPluginSettings(d) })
		cleared, err := repo.SoftRemove(r.Context(), id)
		if err != nil {
			fail(w, r, err)
			return
		}
		cardreaders.SharedCache().Reset()
		readerAudit(d, r, id, "card_reader_removed", map[string]any{"legacy_setting_cleared": cleared})
		renderBody(w, r, http.StatusOK, "card_reader.settings.saved", "ok", "")
	})
}

// readerNeededByAPayment: the registry row is in flight, or a locked tender on
// the till's or the walk-up kiosk's engine is pinned to it.
func readerNeededByAPayment(d *common.Deps, id string) bool {
	if cardreaders.SharedInFlight().Has(id) {
		return true
	}
	for _, e := range []*pos.Service{d.Engine, d.KioskEngine} {
		if e == nil || e.TenderLock() == "" {
			continue
		}
		for _, u := range e.PendingTender().Unknown {
			if u.ReaderRowID == id {
				return true
			}
		}
	}
	return false
}

// bumpPluginSettings invalidates plugin-settings caches after a registry
// removal cleared a legacy `*_reader_id` setting.
func bumpPluginSettings(d *common.Deps) {
	if d != nil && d.Db != nil {
		plugins.SharedBus(d.Db).BumpGeneration()
	}
}

// cardReaderGridView is what the sale screen's tender grid needs: whether one
// "Card" button replaces the managed methods' own buttons (ut-docs#6566 UX
// 2.1), whether it leads (the shop's preferred method is one of them) and the
// last used reader's name (a plain DB read at render: no provider call).
type cardReaderGridView struct {
	Show        bool
	First       bool
	DefaultName string
	// LegacyCard: the grid still carries the generic "card" method (no reader
	// involved), so two buttons would both read "Card"; the template then
	// labels the reader one "Card reader" (O1, #6566).
	LegacyCard bool
}

// cardReaderGrid removes the managed (multi-reader) methods from the grid's
// per-provider buttons. A method with no registry row, or whose plugin does
// not declare the readers hook, keeps its own button exactly as before.
func cardReaderGrid(ctx context.Context, d *common.Deps, grid []data.PaymentMethod, preferred string) ([]data.PaymentMethod, cardReaderGridView) {
	var view cardReaderGridView
	repo := data.NewCardReaderRepo(d.Db)
	rows, err := repo.ListActive(ctx)
	if err != nil || len(rows) == 0 {
		return grid, view
	}
	plug := data.NewPluginRepo(d.Db)
	managed := map[string]bool{}
	checked := map[string]bool{}
	newest, newestAt := "", ""
	for _, c := range rows {
		if !checked[c.MethodKey] {
			checked[c.MethodKey] = true
			ok, err := plug.HasActiveHook(ctx, c.PluginID, readersHook(c.MethodKey))
			managed[c.MethodKey] = err == nil && ok
		}
		if !managed[c.MethodKey] || !readerEligible(c, readerSourcePOS) {
			continue
		}
		view.Show = true
		if c.LastUsedAt != "" && c.LastUsedAt > newestAt {
			newest, newestAt = c.Name, c.LastUsedAt
		}
	}
	if !view.Show {
		return grid, view
	}
	view.DefaultName = newest
	kept := make([]data.PaymentMethod, 0, len(grid))
	for _, m := range grid {
		if managed[m.ID] {
			if m.ID == preferred {
				view.First = true
			}
			continue
		}
		if m.ID == "card" {
			view.LegacyCard = true
		}
		kept = append(kept, m)
	}
	if len(kept) == len(grid) { // no grid method is managed (e.g. only a voucher grid): nothing to replace
		view.Show = false
		return grid, view
	}
	if len(kept) == 0 {
		view.First = true
	}
	return kept, view
}
