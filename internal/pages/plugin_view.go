package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pluginview"
)

// Plugin view pages (ADR-0121 §7, ut-docs#3160; format: ut-docs
// reference/plugin-views.md). A page entry with a `view` is drawn from the
// plugin's answer to ui.view.ask (GET) / ui.action.ask (POST) by core's own
// partials. A slow, broken or invalid answer renders the page chrome with
// an "unavailable" notice — it never blocks anything else.

// pluginViewTimeout bounds one view/action ask, on top of the WASM
// runtime's own per-event deadline (whichever is shorter wins). A var so
// tests can shorten it.
var pluginViewTimeout = 5 * time.Second

// Bounds on what a GET forwards to the plugin as params.
const (
	pluginViewMaxParams        = 20
	pluginViewMaxParamKeyBytes = 64
	pluginViewMaxParamBytes    = 256
)

// The failure notice is the locale key plugin.view.unavailable, rendered
// by web/ui/partials/pluginview/document.html.
const (
	pluginViewAskEvent   = "ui.view.ask"
	pluginActionAskEvent = "ui.action.ask"
	pluginViewPermission = "ui:page"
)

// pluginOwnLocaleKeys returns the keys of the plugin's own locale bundle.
// Overridable in tests (which run without a plugins.Manager).
var pluginOwnLocaleKeys = func(d *common.Deps, pluginID string) map[string]bool {
	if d.Pm == nil {
		return nil
	}
	return d.Pm.OwnLocaleKeys(pluginID)
}

var pluginViewFiles = []string{
	filepath.Join("web", "ui", "layouts", "base.html"),
	filepath.Join("web", "ui", "pages", "plugin_view.html"),
	filepath.Join("web", "ui", "partials", "nav.html"),
	filepath.Join("web", "ui", "partials", "bugreport_panel.html"),
	filepath.Join("web", "ui", "partials", "pos_alert.html"),
	filepath.Join("web", "ui", "partials", "pluginview", "document.html"),
	filepath.Join("web", "ui", "partials", "pluginview", "data.html"),
	filepath.Join("web", "ui", "partials", "pluginview", "controls.html"),
}

// pluginViewMethodAllowed gates a view entry: GET/HEAD ask for the view,
// POST runs an action. Same session (SameSite=Lax cookie, auth.Middleware)
// as every other POST route. Otherwise 405 with Allow.
func pluginViewMethodAllowed(w http.ResponseWriter, r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost:
		return true
	}
	w.Header().Set("Allow", "GET, HEAD, POST")
	http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	return false
}

// pluginViewBody is what pluginview_body renders.
type pluginViewBody struct {
	Route       string
	View        pluginview.View
	Unavailable bool
}

// errPluginNoAnswer: the plugin is not subscribed, or declined to answer.
var errPluginNoAnswer = errors.New("plugin gave no answer")

func servePluginView(w http.ResponseWriter, r *http.Request, d *common.Deps, entry data.PageEntryRow) {
	locale := httpx.ResolveLocale(w, r)
	htmx := r.Header.Get("HX-Request") == "true"
	body := pluginViewBody{Route: entry.Route}
	failStatus := http.StatusBadGateway

	var doc *pluginview.Document
	var redirect string
	var err error
	if r.Method == http.MethodPost {
		var sub pluginview.Submission
		sub, err = readPluginViewForm(w, r)
		if err != nil {
			failStatus = http.StatusBadRequest
		} else {
			doc, redirect, err = askPluginAction(r.Context(), d, entry, locale, sub)
		}
	} else {
		doc, err = askPluginView(r.Context(), d, entry, locale, pluginViewParams(r))
	}

	if err == nil && redirect != "" {
		if htmx {
			w.Header().Set("HX-Redirect", redirect)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}
	if err != nil {
		// The reason stays in the log; the operator sees a translated notice.
		logging.L().Warnf("plugin view %s %q (%s %s): %v", entry.PluginID, entry.View, r.Method, entry.Route, err)
		body.Unavailable = true
	} else {
		body.View = doc.Prepare(locale)
	}

	title := httpx.T(locale, entry.Label)
	docTitle := body.View.Title != ""
	if docTitle {
		title = body.View.Title
		body.View.Title = "" // shown once, as the page heading
	}
	funcs := httpx.FuncsFor(locale)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if r.Method == http.MethodPost && htmx {
		if body.Unavailable {
			w.WriteHeader(failStatus)
		}
		httpx.RenderWith(pluginViewFiles, funcs)("pluginview_body", body)(w, r)
		if docTitle {
			// Only the body is swapped; a new document title reaches the
			// page heading out of band.
			httpx.RenderWith(pluginViewFiles, funcs)("pluginview_title_oob", title)(w, r)
		}
		return
	}
	page := map[string]any{
		"title":     title,
		"theme":     d.CurrentState().Theme,
		"menuItems": d.MenuSnapshot(),
		"pv":        body,
	}
	if httpx.IsFragmentSwap(w, r) {
		if body.Unavailable {
			w.WriteHeader(failStatus)
		}
		httpx.RenderWith(pluginViewFiles, funcs)("content", page)(w, r)
		return
	}
	httpx.RenderWith(pluginViewFiles, funcs)("base", page)(w, r)
}

// pluginViewParams forwards the query string, bounded: the first value of
// at most pluginViewMaxParams keys (sorted), oversize keys/values dropped.
func pluginViewParams(r *http.Request) map[string]string {
	q := r.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := map[string]string{}
	for _, k := range keys {
		if len(out) == pluginViewMaxParams {
			break
		}
		v := q.Get(k)
		if k == "" || len(k) > pluginViewMaxParamKeyBytes || len(v) > pluginViewMaxParamBytes {
			continue
		}
		out[k] = v
	}
	return out
}

func readPluginViewForm(w http.ResponseWriter, r *http.Request) (pluginview.Submission, error) {
	r.Body = http.MaxBytesReader(w, r.Body, pluginview.MaxFormBytes)
	if err := r.ParseForm(); err != nil {
		return pluginview.Submission{}, fmt.Errorf("read form: %w", err)
	}
	c := httpx.ActiveCurrency()
	return pluginview.DecodeForm(r.PostForm, c.Decimals, c.Code)
}

// pluginViewContext is what the plugin's answer is validated against: its
// own locale keys and its own page routes.
func pluginViewContext(ctx context.Context, d *common.Deps, pluginID string) (pluginview.Context, error) {
	entries, err := data.NewPluginRepo(d.Db).ListPageEntries(ctx)
	if err != nil {
		return pluginview.Context{}, err
	}
	c := pluginview.Context{OwnKeys: pluginOwnLocaleKeys(d, pluginID)}
	for _, e := range entries {
		if e.PluginID != pluginID || e.Route == "" {
			continue
		}
		if _, reserved := plugins.ReservedPageRoutePrefix(e.Route); reserved {
			continue
		}
		c.OwnRoutes = append(c.OwnRoutes, e.Route)
	}
	return c, nil
}

func askPluginView(ctx context.Context, d *common.Deps, entry data.PageEntryRow, locale string, params map[string]string) (*pluginview.Document, error) {
	raw, vctx, err := askPluginUI(ctx, d, entry, pluginViewAskEvent, map[string]any{
		"view":   entry.View,
		"params": params,
		"locale": locale,
	})
	if err != nil {
		return nil, err
	}
	return pluginview.DecodeViewAnswer(raw, vctx)
}

func askPluginAction(ctx context.Context, d *common.Deps, entry data.PageEntryRow, locale string, sub pluginview.Submission) (*pluginview.Document, string, error) {
	invalid := sub.Invalid
	if invalid == nil {
		invalid = []string{}
	}
	raw, vctx, err := askPluginUI(ctx, d, entry, pluginActionAskEvent, map[string]any{
		"view":           entry.View,
		"action":         sub.Action,
		"form":           sub.Values,
		"invalid":        invalid,
		"upload_handles": []string{},
		"locale":         locale,
	})
	if err != nil {
		return nil, "", err
	}
	return pluginview.DecodeActionAnswer(raw, vctx)
}

// askPluginUI checks ui:page, then asks exactly the entry's plugin, bounded
// by pluginViewTimeout even if a handler ignores its context.
func askPluginUI(ctx context.Context, d *common.Deps, entry data.PageEntryRow, event string, payload map[string]any) ([]byte, pluginview.Context, error) {
	if err := plugins.CheckPermission(ctx, d.Db, entry.PluginID, pluginViewPermission); err != nil {
		return nil, pluginview.Context{}, err
	}
	vctx, err := pluginViewContext(ctx, d, entry.PluginID)
	if err != nil {
		return nil, vctx, err
	}
	actx, cancel := context.WithTimeout(ctx, pluginViewTimeout)
	defer cancel()
	type result struct {
		raw []byte
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer logging.RecoverAndLog("pages.pluginViewAsk")
		// Sent by a deferred call so a panicking handler still answers at
		// once (it runs before RecoverAndLog recovers).
		res := result{err: errors.New("plugin ask panicked")}
		defer func() { done <- res }()
		raw, ok, err := plugins.SharedBus(d.Db).AskPlugin(actx, entry.PluginID, event, payload)
		res = result{raw, ok, err}
	}()
	select {
	case res := <-done:
		switch {
		case res.err != nil:
			return nil, vctx, res.err
		case !res.ok:
			return nil, vctx, errPluginNoAnswer
		}
		return res.raw, vctx, nil
	case <-actx.Done():
		return nil, vctx, fmt.Errorf("%s: %w", event, actx.Err())
	}
}
