package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/shoptemplate"
	"github.com/universaltill/universal-till/internal/uislot"
)

// Shop type change (ADR-0190, ut-docs#6332 S7): pick -> preview -> confirm
// (PIN-elevated, audited) -> a 7 day undo record. The preview is computed
// by the pure shoptemplate.Diff and writes nothing; the confirm rewrites
// shop.type, the template ledger and only the values the manager ticked
// (the server re-checks each one, the form is never trusted); undo puts
// back only what is still exactly what the change wrote. Main or single
// till only: a till that follows a main till shows the lock text and the
// endpoints answer 409.

const (
	shopTypeSettingsPath = "/settings"
	shopTypeMsgTarget    = "#shop-type-msg"
)

// shopTypeTile is one tile of the shared picker (the wizard's step 5 and
// the pick step here).
type shopTypeTile struct {
	ID, Label, Summary string
	Current            bool // the shop's present type: shown, not selectable
	First              bool // the single tab stop while nothing is selected
}

func shopTypeTiles(d *common.Deps, locale, current string) []shopTypeTile {
	tiles := make([]shopTypeTile, 0, len(setupShopTypes))
	first := true
	for _, id := range setupShopTypes {
		t := shopTypeTile{ID: id, Label: httpx.T(locale, "setup.shop_type."+id), Current: id == current}
		if r := shopTemplateFor(d, id); r != nil && r.SummaryKey() != "" {
			t.Summary = httpx.T(locale, r.SummaryKey())
		}
		if first && !t.Current {
			t.First, first = true, false
		}
		tiles = append(tiles, t)
	}
	return tiles
}

// bufResponse is a throwaway ResponseWriter that keeps the body.
type bufResponse struct {
	h   http.Header
	buf bytes.Buffer
}

func (b *bufResponse) Header() http.Header         { return b.h }
func (b *bufResponse) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufResponse) WriteHeader(int)             {}

// renderShopTypeTiles renders the shared tile group to markup for a page
// that has no shared partial set (the setup wizard renders as one file).
func renderShopTypeTiles(_ http.ResponseWriter, r *http.Request, tiles []shopTypeTile) template.HTML {
	b := &bufResponse{h: http.Header{}}
	httpx.RenderPartial("ui/partials/shop_type_tiles.html", tiles)(b, r)
	return template.HTML(b.buf.String()) // #nosec G203 -- our own partial's output, every value escaped by html/template
}

// shopTypeLabel is the localized tile label, "" for an unset/unknown type.
func shopTypeLabel(locale, id string) string {
	if !isValidShopType(id) {
		return ""
	}
	return httpx.T(locale, "setup.shop_type."+id)
}

// shopTypePanelView is the idle Shop type card body.
type shopTypePanelView struct {
	Label         string
	Satellite     bool
	SatelliteText string
	Tidy          *templateTidyView // legacy shop: "Tidy my menu for {type}" (S8)
}

func shopTypePanelFor(ctx context.Context, d *common.Deps, r *http.Request, locale string) shopTypePanelView {
	cur, _, _ := d.Settings.Get(ctx, common.KeyShopType)
	v := shopTypePanelView{Label: shopTypeLabel(locale, cur)}
	if tillFollowsMain(ctx, d) {
		v.Satellite = true
		v.SatelliteText = shopTypeSatelliteText(ctx, d, locale)
	}
	v.Tidy = templateTidyFor(r, d, locale)
	return v
}

func shopTypeSatelliteText(ctx context.Context, d *common.Deps, locale string) string {
	return strings.ReplaceAll(httpx.T(locale, "settings.template.satellite_lock"), "{till}", mainTillHostPort(d.SyncPrimaryURL(ctx)))
}

// shopTypeBannerView is the 7 day undo banner (Settings landing and Menu).
type shopTypeBannerView struct{ Text string }

// shopTypeUndoBannerFor returns the banner for a manager on the main or
// single till while an unexpired undo record matches the current type; an
// expired record is pruned here (single row, no timer, no cache).
func shopTypeUndoBannerFor(d *common.Deps, r *http.Request, locale string) *shopTypeBannerView {
	if d == nil || d.Settings == nil || !canPerform(d, r, "settings") || tillFollowsMain(r.Context(), d) {
		return nil
	}
	raw, _, err := d.Settings.Get(r.Context(), shoptemplate.UndoKey)
	if err != nil || raw == "" {
		return nil
	}
	u, ok := shoptemplate.ParseUndo(raw, time.Now())
	if !ok {
		if err := d.Settings.Set(r.Context(), shoptemplate.UndoKey, ""); err != nil { // settings-write:allow prune an expired undo record (main/single till only, checked above)
			logging.L().Warnf("shop type undo: prune expired record: %v", err)
		}
		return nil
	}
	if cur, _, _ := d.Settings.Get(r.Context(), common.KeyShopType); cur != u.ToType {
		return nil // the type moved on by another route: nothing to undo here
	}
	return &shopTypeBannerView{Text: strings.ReplaceAll(httpx.T(locale, "settings.template.undo.banner"), "{type}", shopTypeLabel(locale, u.ToType))}
}

// shopTypeUndoneNotice is the one-line status after an undo (?shop_type_undone=N).
func shopTypeUndoneNotice(ctx context.Context, d *common.Deps, r *http.Request, locale string) string {
	raw := r.URL.Query().Get("shop_type_undone")
	if raw == "" {
		return ""
	}
	n, _ := strconv.Atoi(raw)
	cur, _, _ := d.Settings.Get(ctx, common.KeyShopType)
	msg := strings.ReplaceAll(httpx.T(locale, "settings.template.undo.done"), "{type}", shopTypeLabel(locale, cur))
	if n > 0 {
		msg += " " + strings.ReplaceAll(httpx.T(locale, "settings.template.undo.left_alone"), "{count}", strconv.Itoa(n))
	}
	return msg
}

// baseClassFor is the row's class before any template, from the core tables.
func baseClassFor(kind shoptemplate.Kind, key string) uislot.Class {
	var (
		e  uislot.Entry
		ok bool
	)
	switch kind {
	case shoptemplate.KindMenu:
		e, ok = uislot.CoreMenuEntry(key)
	case shoptemplate.KindSettings:
		e, ok = uislot.CoreSettingsEntry(key)
	}
	if !ok {
		return uislot.ClassCommon
	}
	return e.Class
}

// shopTypePlan is everything a change needs, read once.
type shopTypePlan struct {
	oldType  string
	oldRaw   string // the stored ledger, verbatim (for undo)
	state    common.TemplateState
	ledger   shoptemplate.Ledger
	newT     *shoptemplate.Resolved
	current  map[string]string
	preview  shoptemplate.Preview
	noChange bool // same type and nothing left to apply
}

func planShopTypeChange(ctx context.Context, d *common.Deps, to string) (shopTypePlan, error) {
	p := shopTypePlan{state: d.TemplateState(ctx), newT: shopTemplateFor(d, to)}
	var err error
	if p.oldType, _, err = d.Settings.Get(ctx, common.KeyShopType); err != nil {
		return p, err
	}
	if p.oldRaw, _, err = d.Settings.Get(ctx, shoptemplate.LedgerKey); err != nil {
		return p, err
	}
	if p.oldRaw != "" {
		if err := json.Unmarshal([]byte(p.oldRaw), &p.ledger); err != nil {
			p.ledger = shoptemplate.Ledger{} // unreadable ledger: treat nothing as template-owned (safe: nothing offered over a value)
		}
	}
	keys := map[string]bool{}
	for k := range p.ledger.Seeded {
		keys[k] = true
	}
	for _, s := range p.newT.Seeds() {
		keys[s.Key] = true
	}
	p.current = make(map[string]string, len(keys))
	for k := range keys {
		v, _, err := d.Settings.Get(ctx, k)
		if err != nil {
			return p, err
		}
		p.current[k] = v
	}
	p.preview = shoptemplate.Diff(p.state.Effective(), p.newT, p.ledger, p.current, baseClassFor)
	p.noChange = p.oldType == to && (p.state.Mode == common.TemplateActive || p.newT == nil)
	return p, nil
}

type previewRow struct{ Key, Label string }
type previewValueRow struct{ Key, Label, Now, Suggested, NowSuggested string }

func previewLabel(locale string, it shoptemplate.PreviewItem) string {
	var (
		e  uislot.Entry
		ok bool
	)
	switch it.Kind {
	case shoptemplate.KindMenu:
		e, ok = uislot.CoreMenuEntry(it.Key)
	case shoptemplate.KindSettings:
		e, ok = uislot.CoreSettingsEntry(it.Key)
	}
	if !ok || e.LabelKey == "" {
		return it.Key
	}
	return httpx.T(locale, e.LabelKey)
}

func seedKeyLabel(locale, key string) string {
	for _, s := range data.AgentSettings {
		if s.Key == key && s.LabelKey != "" {
			return httpx.T(locale, s.LabelKey)
		}
	}
	return key
}

func kindOf(items []shoptemplate.PreviewItem, k shoptemplate.Kind) []shoptemplate.PreviewItem {
	var out []shoptemplate.PreviewItem
	for _, it := range items {
		if it.Kind == k {
			out = append(out, it)
		}
	}
	return out
}

func previewRows(locale string, items []shoptemplate.PreviewItem) []previewRow {
	out := make([]previewRow, 0, len(items))
	for _, it := range items {
		out = append(out, previewRow{Key: it.Key, Label: previewLabel(locale, it)})
	}
	return out
}

// shopTypePreviewData is the preview partial's input.
func shopTypePreviewData(locale, from, to string, p shoptemplate.Preview) map[string]any {
	vals := make([]previewValueRow, 0, len(p.Values))
	for _, v := range p.Values {
		now := v.Now
		if now == "" {
			now = "—"
		}
		vals = append(vals, previewValueRow{
			Key: v.Key, Label: seedKeyLabel(locale, v.Key), Now: now, Suggested: v.Suggested,
			NowSuggested: strings.NewReplacer("{now}", now, "{suggested}", v.Suggested).Replace(httpx.T(locale, "settings.template.preview.now_suggested")),
		})
	}
	kept := make([]string, 0, len(p.Kept))
	for _, k := range p.Kept {
		kept = append(kept, seedKeyLabel(locale, k))
	}
	titleKey := "settings.template.preview.title"
	if from == to {
		titleKey = "settings.template.preview.title_apply" // a shop that predates templates accepting its own type
	}
	title := strings.NewReplacer("{from}", orDash(shopTypeLabel(locale, from)), "{to}", orDash(shopTypeLabel(locale, to))).Replace(httpx.T(locale, titleKey))
	return map[string]any{
		"To": to, "Title": title,
		"ShownMenu": previewRows(locale, kindOf(p.Shown, shoptemplate.KindMenu)), "ShownSettings": previewRows(locale, kindOf(p.Shown, shoptemplate.KindSettings)), "HiddenMenu": previewRows(locale, kindOf(p.Hidden, shoptemplate.KindMenu)), "HiddenSettings": previewRows(locale, kindOf(p.Hidden, shoptemplate.KindSettings)),
		"Values": vals, "Kept": kept, "KeptHead": firstN(kept, 3), "KeptRest": restAfter(kept, 3),
		"KeptMore":  strings.ReplaceAll(httpx.T(locale, "settings.template.preview.kept_count"), "{count}", strconv.Itoa(len(restAfter(kept, 3)))),
		"LabelOnly": p.LabelOnly(),
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func restAfter(s []string, n int) []string {
	if len(s) <= n {
		return nil
	}
	return s[n:]
}

// shopTypeFragmentError answers an htmx fragment with a role=alert line and
// the given status (app.js force-swaps a text/html 4xx into the hx-target).
func shopTypeFragmentError(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<p class="login-error" role="alert">%s</p>`, html.EscapeString(text))
}

// shopTypeMainOnly refuses on a till that follows a main till (409 + the
// lock text naming the main till). Reports whether the request may go on.
func shopTypeMainOnly(w http.ResponseWriter, r *http.Request, d *common.Deps) bool {
	if !tillFollowsMain(r.Context(), d) {
		return true
	}
	shopTypeFragmentError(w, http.StatusConflict, shopTypeSatelliteText(r.Context(), d, httpx.RequestLocale(r)))
	return false
}

func registerShopTypeChange(mux *http.ServeMux, d *common.Deps, posRepo *data.POSRepo) {
	// gate: a manager (cashier 403 on every one of these, hidden AND refused).
	gate := func(w http.ResponseWriter, r *http.Request) bool {
		return requirePage(d, w, r, "settings") && shopTypeMainOnly(w, r, d)
	}

	mux.HandleFunc("GET /api/settings/shop-type/panel", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "settings") {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		httpx.RenderPartial("ui/partials/shop_type_panel.html", shopTypePanelFor(r.Context(), d, r, locale))(w, r)
	})

	registerTemplateOffer(mux, d)

	mux.HandleFunc("GET /api/settings/shop-type/pick", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		cur, _, _ := d.Settings.Get(r.Context(), common.KeyShopType)
		// RenderBlock: the pick step includes the shared tile partial by name.
		httpx.RenderBlock("ui/partials/shop_type_pick.html", "shop_type_pick.html", map[string]any{
			"Tiles": shopTypeTiles(d, locale, cur),
		})(w, r)
	})

	mux.HandleFunc("GET /api/settings/shop-type/preview", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		to := strings.TrimSpace(r.URL.Query().Get("to"))
		if to == "" || !isValidShopType(to) {
			http.Error(w, "unknown shop type", http.StatusBadRequest)
			return
		}
		plan, err := planShopTypeChange(r.Context(), d, to)
		if err != nil {
			logging.L().Errorf("shop type preview: %v", err)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			httpx.RenderPartial("ui/partials/shop_type_preview.html", map[string]any{"To": to, "Error": true})(w, r)
			return
		}
		httpx.RenderPartial("ui/partials/shop_type_preview.html", shopTypePreviewData(locale, plan.oldType, to, plan.preview))(w, r)
	})

	// Confirm. Accepts `to` (the preview's form) or the older `shop_type`
	// field; an empty value clears the type. apply_values[] are the keys the
	// manager ticked; only those the server's own diff offers are written.
	mux.HandleFunc("POST /api/settings/shop-type", func(w http.ResponseWriter, r *http.Request) {
		// Validate BEFORE the elevation gate (ut-docs#557 convention) — a
		// bad shop type 400s without burning an approver's live PIN entry.
		_ = r.ParseForm()
		raw := r.Form.Get("shop_type")
		if _, ok := r.Form["to"]; ok {
			raw = r.Form.Get("to")
		}
		v := strings.TrimSpace(raw)
		if v != "" && !isValidShopType(v) {
			http.Error(w, "unknown shop type", http.StatusBadRequest)
			return
		}
		if !shopTypeMainOnly(w, r, d) {
			return
		}
		ticked := r.Form["apply_values[]"]
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			hidden := []elevationHiddenField{{Name: "to", Value: v}}
			for _, k := range ticked {
				hidden = append(hidden, elevationHiddenField{Name: "apply_values[]", Value: k})
			}
			renderElevationPrompt(w, r, "/api/settings/shop-type", shopTypeMsgTarget,
				fmt.Sprintf(httpx.T(locale, "elevation.summary.shop_type"), orDash(shopTypeLabel(locale, v))),
				hidden, elev)
			return
		}
		locale := httpx.RequestLocale(r)
		plan, err := planShopTypeChange(r.Context(), d, v)
		if err != nil {
			logging.L().Errorf("shop type change: plan: %v", err)
			shopTypeFragmentError(w, http.StatusInternalServerError, httpx.T(locale, "settings.template.preview.apply_error"))
			return
		}
		if plan.noChange {
			// A double tap (or a retry): the first call already did it.
			w.Header().Set("HX-Redirect", shopTypeSettingsPath)
			settingsRespondSaved(w, r, elev)
			return
		}

		// Only keys the diff itself offers, never what the form names.
		offered := map[string]string{}
		for _, vc := range plan.preview.Values {
			offered[vc.Key] = vc.Suggested
		}
		applied, prev := map[string]string{}, map[string]string{}
		for _, k := range ticked {
			if sv, ok := offered[k]; ok {
				applied[k], prev[k] = sv, plan.current[k]
			}
		}

		writes := map[string]string{common.KeyShopType: v}
		for k, val := range applied {
			writes[k] = val
		}
		ledgerRaw := ""
		if plan.newT != nil {
			seeded := map[string]string{}
			for k, val := range plan.ledger.Seeded { // still exactly what the template wrote: stays template-owned
				if _, replaced := applied[k]; !replaced && plan.current[k] == val {
					seeded[k] = val
				}
			}
			for k, val := range applied {
				seeded[k] = val
			}
			b, _ := json.Marshal(shoptemplate.Ledger{ID: plan.newT.ID(), Version: plan.newT.Version(), Seeded: seeded, At: time.Now().UTC().Format(time.RFC3339)})
			ledgerRaw = string(b)
		}
		writes[shoptemplate.LedgerKey] = ledgerRaw
		writes[shoptemplate.UndoKey] = shoptemplate.NewUndo(v, plan.oldType, plan.oldRaw, applied, prev, time.Now()).Encode()

		// main/single till only (shopTypeMainOnly above), one transaction.
		if err := d.Settings.SetMany(r.Context(), writes); err != nil { // settings-write:allow shop type change on the main/single till (S7), PIN-elevated and audited below
			logging.L().Errorf("shop type change: write: %v", err)
			shopTypeFragmentError(w, http.StatusInternalServerError, httpx.T(locale, "settings.template.preview.apply_error"))
			return
		}
		d.ReloadTemplate(r.Context())
		if val, ok := applied[data.OrderTypePromptModeKey]; ok {
			httpx.InitOrderTypePromptMode(val)
		}
		keys := make([]string, 0, len(applied))
		for k := range applied {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		settingsAudit(r, posRepo, elev, "settings", common.KeyShopType, "shop_type_changed",
			map[string]any{"shop_type": v, "from": plan.oldType, "values": keys})
		w.Header().Set("HX-Redirect", shopTypeSettingsPath)
		settingsRespondSaved(w, r, elev)
	})

	mux.HandleFunc("POST /api/settings/shop-type/undo", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		_ = r.ParseForm()
		locale := httpx.RequestLocale(r)
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/settings/shop-type/undo", "#shop-type-undo-msg",
				httpx.T(locale, "settings.template.undo.undo"), nil, elev)
			return
		}
		raw, _, err := d.Settings.Get(r.Context(), shoptemplate.UndoKey)
		u, ok := shoptemplate.ParseUndo(raw, time.Now())
		cur, _, _ := d.Settings.Get(r.Context(), common.KeyShopType)
		if err != nil || !ok || cur != u.ToType {
			shopTypeFragmentError(w, http.StatusConflict, httpx.T(locale, "settings.template.undo.error"))
			return
		}
		writes := map[string]string{
			common.KeyShopType:     u.PrevType,
			shoptemplate.LedgerKey: u.PrevLedger,
			shoptemplate.UndoKey:   "",
		}
		left := 0
		for k, applied := range u.Applied {
			if !shoptemplate.IsSeedable(k) {
				continue // the stored undo record is data: only a seedable key is ever written back
			}
			now, _, _ := d.Settings.Get(r.Context(), k)
			if now == applied {
				writes[k] = u.Prev[k]
			} else {
				left++ // edited since: the owner's value stays
			}
		}
		if err := d.Settings.SetMany(r.Context(), writes); err != nil { // settings-write:allow undo of a shop type change (main/single till, PIN-elevated, audited)
			logging.L().Errorf("shop type undo: write: %v", err)
			shopTypeFragmentError(w, http.StatusInternalServerError, httpx.T(locale, "settings.template.undo.error"))
			return
		}
		d.ReloadTemplate(r.Context())
		if val, ok := writes[data.OrderTypePromptModeKey]; ok {
			httpx.InitOrderTypePromptMode(val) // "" (the pre-change unset value) falls back to "top", as at boot
		}
		settingsAudit(r, posRepo, elev, "settings", common.KeyShopType, "shop_type_undone",
			map[string]any{"shop_type": u.PrevType, "from": u.ToType, "left_alone": left})
		w.Header().Set("HX-Redirect", shopTypeSettingsPath+"?shop_type_undone="+strconv.Itoa(left))
		settingsRespondSaved(w, r, elev)
	})

	mux.HandleFunc("POST /api/settings/shop-type/dismiss-banner", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		if err := d.Settings.Set(r.Context(), shoptemplate.UndoKey, ""); err != nil { // settings-write:allow dismiss the undo banner (main/single till only, gated above)
			shopTypeFragmentError(w, http.StatusInternalServerError, httpx.T(httpx.RequestLocale(r), "settings.template.undo.error"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK) // empty body: htmx swaps the banner away
	})
}
