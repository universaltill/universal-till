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
//
// A failed action (unreadable post, failed/slow/invalid ui.action.ask
// answer, or a job refused as busy) never costs the operator their input
// (ut-docs#3879): an htmx action answers HX-Reswap: none plus the notice
// out of band into #plugin-view-alert, so the form (file and password
// inputs included) stays as typed; the no-JS post re-asks ui.view.ask and
// renders that view with the posted values refilled
// (pluginview.View.Refill) under the notice.

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
// by web/ui/partials/pluginview/document.html; a failed action shows
// plugin.view.action_failed (or plugin.job.busy) in pluginview_alert
// (web/ui/pages/plugin_view.html) instead.
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
	filepath.Join("web", "ui", "partials", "pluginview", "poll.html"),
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

// pluginViewBody is what pluginview_body renders: the view, the
// unavailable notice, a running job's poll, or a job notice (busy: the
// plugin already runs its maximum jobs; gone: the polled job is unknown,
// expired or not this route's).
type pluginViewBody struct {
	Route       string
	View        pluginview.View
	Unavailable bool
	Job         *pluginJobPoll
	JobBusy     bool
	JobGone     bool
	// Target is the id of the element this body is drawn in and its
	// actions/poll swap into: "" for the page's own #plugin-view, or a
	// content slot panel's id (ut-docs#3872, pluginSlotPanelIDRe).
	Target string
	// ReadOnly draws no actions or forms (setup.wizard.steps: the wizard
	// is auth-exempt, /plugin/ routes are not -- ut-docs#3872).
	ReadOnly bool
}

// TargetID is the id actions, forms and the job poll target.
func (b pluginViewBody) TargetID() string {
	if b.Target == "" {
		return "plugin-view"
	}
	return b.Target
}

// pluginViewAlert is what pluginview_alert renders into an action's
// failure slot: its notice, or nothing. OOB marks the htmx out-of-band
// copy. ID is the slot's id: "" for the page's #plugin-view-alert, or a
// content slot panel's "<panel id>-alert" (slot.html).
type pluginViewAlert struct {
	ID     string
	OOB    bool
	Failed bool // plugin.view.action_failed
	Busy   bool // plugin.job.busy
}

// pluginJobPoll is what pluginview_poll renders.
type pluginJobPoll struct {
	ID      string
	HasPct  bool // false: indeterminate progress (nothing reported yet)
	Pct     int
	Message string // the plugin's job_progress message, translated; "" = core's
}

// errPluginNoAnswer: the plugin is not subscribed, or declined to answer.
var errPluginNoAnswer = errors.New("plugin gave no answer")

func servePluginView(w http.ResponseWriter, r *http.Request, d *common.Deps, entry data.PageEntryRow) {
	locale := httpx.ResolveLocale(w, r)
	htmx := r.Header.Get("HX-Request") == "true"
	body := pluginViewBody{Route: entry.Route}
	failStatus := http.StatusBadGateway
	_, isPoll := r.URL.Query()[pluginJobParam]
	isPoll = isPoll && r.Method != http.MethodPost
	fragment := httpx.IsFragmentSwap(w, r)
	// An action or poll from a content slot panel (ut-docs#3872) answers
	// into that panel: htmx names it in HX-Target. Anything else -- no
	// header, an id that is not a slot panel's, or a request answered with
	// the whole page -- is the page's own container, so a request can
	// never point the answer at core markup.
	inSlot := false
	if t := r.Header.Get("HX-Target"); htmx && (r.Method == http.MethodPost || (isPoll && fragment)) && pluginSlotPanelIDRe.MatchString(t) {
		body.Target = t
		inSlot = true
	}
	// A panel is drawn under ui:slot:<slot> (contentSlotPanels), so its
	// actions run under that same grant, not ui:page (ut-docs#3963). No
	// slot target, or an entry with no slot, stays on ui:page.
	actionPerm := pluginViewPermission
	if inSlot && entry.Slot != "" {
		actionPerm = "ui:slot:" + entry.Slot
		// /plugin/ routes need only a session; a panel's action is also
		// gated by its host screen, as GET /ui/slot/{slot} is (fail
		// closed: setup.wizard.steps has no gate and draws no actions).
		if gate, ok := pluginSlotGates[entry.Slot]; r.Method == http.MethodPost && (!ok || !gate(d, r)) {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
	}

	var doc *pluginview.Document
	var redirect string
	var err error
	// The action post, kept for a no-JS refill; formRead: it parsed.
	var sub pluginview.Submission
	formRead := false
	switch {
	case isPoll:
		// A job's poll (ADR-0121 §8): never asks the plugin.
		snap, ok := pluginJobs.poll(r.URL.Query().Get(pluginJobParam), entry.PluginID, entry.Route, r.Method != http.MethodHead)
		switch {
		case !ok:
			body.JobGone = true
		case snap.state == pluginJobRunning && snap.unchanged && htmx && fragment:
			// Nothing new since the last poll: 204, so htmx swaps nothing
			// and the poll's live region (and its every-1s trigger) stays
			// put instead of being re-announced every second. The no-JS
			// page (no HX-Request) always renders.
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNoContent)
			return
		case snap.state == pluginJobRunning:
			body.Job = &pluginJobPoll{ID: r.URL.Query().Get(pluginJobParam), HasPct: snap.pct >= 0, Pct: snap.pct}
			if snap.key != "" {
				body.Job.Message = httpx.T(locale, snap.key)
			}
		case snap.state == pluginJobFailed:
			err = errors.New("job failed (reason logged when it ended)")
		default:
			doc, redirect = snap.doc, snap.redirect
		}
	case r.Method == http.MethodPost:
		var ups []pluginUpload
		sub, ups, err = readPluginViewForm(w, r, entry)
		if err != nil {
			failStatus = pluginViewFormFailStatus(err)
			break
		}
		formRead = true
		// Staged uploads (ut-docs#3793) never outlive this ask — unless
		// the answer is a job, which then owns them until it ends.
		tokens := uploadTokens(ups)
		jobOwnsUploads := false
		defer func() {
			if !jobOwnsUploads {
				plugins.ReleaseUploads(entry.PluginID, tokens)
			}
		}()
		var ans pluginview.ActionAnswer
		var vctx pluginview.Context
		var payload map[string]any
		ans, vctx, payload, err = askPluginAction(r.Context(), d, entry, actionPerm, locale, sub, ups)
		switch {
		case err != nil:
		case ans.Job != "":
			var id string
			id, err = startPluginJob(r.Context(), d, entry, ans.Job, payload, vctx, tokens)
			jobOwnsUploads = err == nil
			switch {
			case errors.Is(err, errPluginJobBusy):
				err = nil
				body.JobBusy = true
				failStatus = http.StatusTooManyRequests
			case err == nil:
				body.Job = &pluginJobPoll{ID: id}
			}
		default:
			doc, redirect = ans.Document, ans.Redirect
		}
	default:
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
	switch {
	case err != nil:
		// The reason stays in the log; the operator sees a translated notice.
		logging.L().Warnf("plugin view %s %q (%s %s): %v", entry.PluginID, entry.View, r.Method, entry.Route, err)
		body.Unavailable = true
	case doc != nil:
		body.View = doc.Prepare(locale)
	}
	failed := body.Unavailable || body.JobBusy
	if isPoll {
		// A poll answer always replaces the poll, whatever happened.
		failed = false
	}
	// A failed action (ut-docs#3879): the notice goes to #plugin-view-alert
	// and the view stays — untouched (htmx) or re-asked and refilled (no JS).
	actionFailed := failed && r.Method == http.MethodPost
	alertID := ""
	if inSlot {
		alertID = body.Target + "-alert"
	}
	alert := pluginViewAlert{ID: alertID}
	if actionFailed {
		alert = pluginViewAlert{ID: alertID, Busy: body.JobBusy, Failed: !body.JobBusy}
	}
	if actionFailed && !htmx {
		var posted map[string][]string
		action := ""
		if formRead {
			// An unread post may be half decoded: refill nothing from it.
			action, posted = sub.Action, sub.Posted
		}
		if v, ok := pluginViewRefilled(r.Context(), d, entry, locale, action, posted); ok {
			body = pluginViewBody{Route: entry.Route, View: v}
		} else {
			// No view to show: the body's own notice, as before.
			alert = pluginViewAlert{}
		}
	}

	title := httpx.T(locale, entry.Label)
	docTitle := body.View.Title != ""
	if inSlot {
		// A slot panel's heading is the body's own title (pluginview_slot),
		// kept on a failed, busy or job answer too so the operator still
		// sees which plugin's panel it is (ut-docs#3872 review).
		if !docTitle {
			body.View.Title = title
		}
		docTitle = false
	} else if docTitle {
		title = body.View.Title
		body.View.Title = "" // shown once, as the page heading
	}
	funcs := httpx.FuncsFor(locale)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if isPoll {
		w.Header().Set("Cache-Control", "no-store")
	}

	if htmx && (r.Method == http.MethodPost || (isPoll && fragment)) {
		render := httpx.RenderWith(pluginViewFiles, funcs)
		if actionFailed {
			// Swap nothing (the form keeps the operator's input, file and
			// password fields included; submitting again is the retry) —
			// only the alert, out of band. htmx 1.9 still runs OOB swaps
			// under swap style none, and app.js's htmx:beforeSwap
			// force-swaps this non-2xx text/html answer (shouldSwap), so
			// they are processed (ut-docs#3879).
			alert.OOB = true
			w.Header().Set("HX-Reswap", "none")
			w.WriteHeader(failStatus)
			render("pluginview_alert", alert)(w, r)
			return
		}
		render("pluginview_body", body)(w, r)
		if docTitle {
			// Only the body is swapped; a new document title reaches the
			// page heading out of band.
			render("pluginview_title_oob", title)(w, r)
		}
		// Any other answer clears a stale failure notice.
		render("pluginview_alert", pluginViewAlert{ID: alertID, OOB: true})(w, r)
		return
	}
	page := map[string]any{
		"title":     title,
		"theme":     d.CurrentState().Theme,
		"menuItems": d.MenuSnapshot(),
		"pv":        body,
		"alert":     alert,
	}
	if fragment {
		if failed {
			w.WriteHeader(failStatus)
		}
		httpx.RenderWith(pluginViewFiles, funcs)("content", page)(w, r)
		return
	}
	httpx.RenderWith(pluginViewFiles, funcs)("base", page)(w, r)
}

// pluginViewRefilled re-asks the entry's view (no params) after a no-JS
// action failed and refills the form of action with posted (nil: refill
// nothing). ok is false when the view cannot be shown either.
func pluginViewRefilled(ctx context.Context, d *common.Deps, entry data.PageEntryRow, locale, action string, posted map[string][]string) (pluginview.View, bool) {
	doc, err := askPluginView(ctx, d, entry, locale, map[string]string{})
	if err != nil {
		logging.L().Warnf("plugin view %s %q (re-ask after a failed action, %s): %v", entry.PluginID, entry.View, entry.Route, err)
		return pluginview.View{}, false
	}
	v := doc.Prepare(locale)
	if posted != nil {
		v.Refill(action, posted)
	}
	return v, true
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
		if k == pluginJobParam {
			continue // core's job poll parameter (ADR-0121 §8), never the plugin's
		}
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

// readPluginViewForm reads an action post: url-encoded, or multipart when
// the form has a file field (readPluginViewMultipart, ut-docs#3793). The
// uploads are never nil on success ([] without files).
func readPluginViewForm(w http.ResponseWriter, r *http.Request, entry data.PageEntryRow) (pluginview.Submission, []pluginUpload, error) {
	if isMultipartForm(r) {
		return readPluginViewMultipart(w, r, entry)
	}
	r.Body = http.MaxBytesReader(w, r.Body, pluginview.MaxFormBytes)
	if err := r.ParseForm(); err != nil {
		return pluginview.Submission{}, nil, fmt.Errorf("read form: %w", err)
	}
	c := httpx.ActiveCurrency()
	sub, err := pluginview.DecodeForm(r.PostForm, c.Decimals, c.Code)
	return sub, []pluginUpload{}, err
}

// pluginViewContext is what the plugin's answer is validated against: its
// own locale keys and its own page routes.
func pluginViewContext(ctx context.Context, d *common.Deps, pluginID string) (pluginview.Context, error) {
	entries, err := data.NewPluginRepo(d.Db).ListPageEntries(ctx)
	if err != nil {
		return pluginview.Context{}, err
	}
	c := pluginview.Context{PluginID: pluginID, OwnKeys: pluginOwnLocaleKeys(d, pluginID)}
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

// askPluginAction asks ui.action.ask; it also returns the validation
// context and the payload, which a job answer reuses (startPluginJob). It
// checks perm: ui:page, or ui:slot:<slot> for a slot panel's action.
func askPluginAction(ctx context.Context, d *common.Deps, entry data.PageEntryRow, perm, locale string, sub pluginview.Submission, ups []pluginUpload) (pluginview.ActionAnswer, pluginview.Context, map[string]any, error) {
	invalid := sub.Invalid
	if invalid == nil {
		invalid = []string{}
	}
	if ups == nil {
		ups = []pluginUpload{}
	}
	payload := map[string]any{
		"view":           entry.View,
		"action":         sub.Action,
		"form":           sub.Values,
		"invalid":        invalid,
		"upload_handles": ups,
		"locale":         locale,
	}
	raw, vctx, err := askPluginUIAs(ctx, d, entry, perm, pluginViewTimeout, pluginActionAskEvent, payload)
	if err != nil {
		return pluginview.ActionAnswer{}, vctx, nil, err
	}
	ans, err := pluginview.DecodeActionAnswer(raw, vctx)
	return ans, vctx, payload, err
}

// askPluginUI checks ui:page, then asks exactly the entry's plugin,
// bounded by pluginViewTimeout (a view page or an action).
func askPluginUI(ctx context.Context, d *common.Deps, entry data.PageEntryRow, event string, payload map[string]any) ([]byte, pluginview.Context, error) {
	return askPluginUIAs(ctx, d, entry, pluginViewPermission, pluginViewTimeout, event, payload)
}

// askPluginUIAs checks perm, then asks exactly the entry's plugin, bounded
// by timeout even if a handler ignores its context. A page asks under
// ui:page (askPluginUI); a content slot under ui:slot:<slot>
// (contentSlotPanels, ut-docs#3872).
func askPluginUIAs(ctx context.Context, d *common.Deps, entry data.PageEntryRow, perm string, timeout time.Duration, event string, payload map[string]any) ([]byte, pluginview.Context, error) {
	if err := plugins.CheckPermission(ctx, d.Db, entry.PluginID, perm); err != nil {
		return nil, pluginview.Context{}, err
	}
	vctx, err := pluginViewContext(ctx, d, entry.PluginID)
	if err != nil {
		return nil, vctx, err
	}
	// A file field is only valid on an entry that declared its size cap.
	vctx.Uploads = entry.UploadMaxMB > 0
	actx, cancel := context.WithTimeout(ctx, timeout)
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
