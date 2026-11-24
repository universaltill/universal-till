package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paybroker"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6458 (ADR-0198 3.1, 6; ADR-0203): Settings > Payments > Tap to Pay on
// iPhone. A manager prepares this iPhone for card taps in four steps: the
// iPhone itself, Apple's terms and location (the native Setup), the Stripe
// account (read-only here: the key is set on the web) and the explicit grant
// of the two permissions the Stripe payment plugin needs. Nothing on the page
// is a secret: it shows the mode and the source of the Stripe account only.

// tapSetupTimeout bounds one native Setup run (Apple's sheet, the terms, the
// connect). It is detached from the HTTP request so a navigating client does
// not cancel Apple's sheet. A var so a test can shorten it.
var tapSetupTimeout = 600 * time.Second

// tapCloudTTL is how long the cloud's answer is reused by the polling status
// route (the page itself always asks fresh).
const tapCloudTTL = 10 * time.Second

// tapGrantPermissions is the fixed allow-list of the grant route: exactly
// these two permissions, on exactly the plugin that owns the provider's tap
// entry. The form carries no permission name (no generic grant surface).
var tapGrantPermissions = []string{plugins.TapDevicePermission, plugins.BrokerNetPermission}

// tapCloud is what the panel reads from the broker's onboarding_status.
type tapCloud struct {
	Known  bool // the cloud answered
	Ready  bool
	Source string // own | connect
	Live   bool
	Reason string
	// ErrCode is the stable code of a failed broker call (Known is false);
	// shown to support under "Details", never as prose.
	ErrCode string
}

// tapStep is one line of the checklist. The slice order is the order a
// manager does them: device, allow, apple, account (ut-docs#6981).
type tapStep struct {
	ID       string // device | allow | apple | account
	Done     bool
	Unknown  bool   // the state could not be read (never "not set up")
	LineKey  string // i18n key of the one muted line
	Action   string // "" | grant | setup
	ActionOK bool
	// HintKey is the i18n key of the visible reason a present Action is
	// disabled ("" when it is enabled, or when the status block already says
	// why). Never a silently disabled button (ADR-0198 3.8).
	HintKey string
	Busy    bool   // the Setup is running (aria-busy)
	IsNext  bool   // this step's button is the status block's one button
	Detail  string // raw code under "Details for support" (account step)
}

type tapSettingsView struct {
	Available  bool   // a native bridge is registered
	NotMain    bool   // this till cannot take Tap to Pay (a satellite or a till with no recorded role)
	Additional bool   // an explicitly additional till: the panel adds its note
	State      string // ready | preparing | needs_setup | unavailable
	Icon       string
	ReasonKey  string
	Test       bool // the Stripe account answered in test mode
	Steps      []tapStep
	Next       *tapStep // the step whose button is shown in the status block
	Progress   int
	Running    bool
	NoticeKey  string // set when the last Setup failed
	NoticeWhy  string // i18n key of the reason of that failure
	// NoticeDetail is the raw code of that failure for "Details for support".
	NoticeDetail string
	DoneNotice   bool // the last Setup finished
	AlreadyMsg   bool
	StoreBuild   bool
}

// tapSetupRun is the one Setup in flight (or the last one that finished).
type tapSetupRun struct {
	running bool
	done    bool
	res     devicepay.SetupResult
}

type tapSettingsCtl struct {
	d  *common.Deps
	mu sync.Mutex
	// setup is the guarded Setup run; cloud the last broker answer.
	setup   tapSetupRun
	cloud   tapCloud
	cloudAt time.Time
}

// tapSettingsAvailable reports whether the panel (and its nav link) is offered:
// only a till with a native Tap to Pay bridge has anything to prepare.
func tapSettingsAvailable() bool { return devicepay.RegisteredBridge() != nil }

// tapNativeReason maps the native bridges' reason strings (iOS and Android
// send terms_not_accepted / location_denied) onto the page's vocabulary; the
// short forms stay accepted.
func tapNativeReason(r string) string {
	switch r {
	case "terms_not_accepted":
		return "terms"
	case "location_denied":
		return "location"
	}
	return r
}

// tapReasonKey is the i18n key of a setup / capability reason or of a broker
// error code (ut-docs#6981). code is passed through tapNativeReason by the
// caller; detail is the native error code of an "other" failure: with one,
// "other" is Apple's or Stripe's own failure.
func tapReasonKey(code, detail string) string {
	switch code {
	case "ipad", "unsupported_device", "ios_too_old", "not_foreground", "not_main", "timeout", "terms", "location", "not_onboarded", "offline":
		return "tap.settings.reason." + code
	case "device_role_forbidden":
		return "tap.settings.reason.not_main"
	case "device_role_unrecorded", "device_role_unconfirmed", "unauthorized", "token_retired", "device_credential_required":
		return "tap.settings.source.setup_again"
	case "not_enrolled", "insecure_endpoint", "entitlement_missing", "busy":
		return "tap.settings.reason." + code
	case "canceled_by_user":
		return "tap.settings.reason.cancelled"
	case "other":
		if detail != "" {
			return "tap.settings.reason.apple_failed"
		}
	case "auth_unavailable", "provider_unavailable", "response_too_large":
		return "tap.settings.reason.service_unavailable"
	case "not_enabled", "disabled":
		return "tap.settings.reason.not_enabled"
	case "account_not_ready", "profile_incomplete":
		return "tap.settings.reason.not_onboarded"
	case "not_connected", "account_disconnected":
		return "tap.settings.reason.not_connected"
	}
	if len(code) == 8 && strings.HasPrefix(code, "http_5") {
		return "tap.settings.reason.service_unavailable"
	}
	return "tap.settings.reason.generic"
}

// tapDetailText is what a failed Setup shows under "Details for support":
// the reason, plus the native error code when there is one.
func tapDetailText(reason, detail string) string {
	if detail == "" || detail == reason {
		return reason
	}
	return reason + " (" + detail + ")"
}

// readCloud asks the broker for the Stripe account's state (fresh = skip the
// short cache). A network or 5xx error is an unknown answer, never "not set up".
func (c *tapSettingsCtl) readCloud(ctx context.Context, fresh bool) tapCloud {
	c.mu.Lock()
	if !fresh && !c.cloudAt.IsZero() && time.Since(c.cloudAt) < tapCloudTTL {
		got := c.cloud
		c.mu.Unlock()
		return got
	}
	c.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := paybroker.Call(cctx, paybroker.CallerCore, devicepay.ProviderStripe, "onboarding_status", []byte("{}"))
	var got tapCloud
	var he *paybroker.HTTPError
	switch {
	case errors.As(err, &he) && (he.Code == paybroker.CodeDeviceRoleUnrecorded || he.Code == paybroker.CodeDeviceRoleUnconfirmed):
		// The cloud refused this till's credential (set up before roles were
		// recorded): a known answer, not "unreachable" (UX copy section 4).
		got = tapCloud{Known: true, Reason: paybroker.CodeDeviceRoleUnrecorded} // unconfirmed reads the same
	case err != nil:
		got = tapCloud{ErrCode: paybroker.ErrorCode(err)}
	}
	if err == nil {
		got = tapCloud{ErrCode: "failed"}
		var a struct {
			Ready    bool   `json:"ready"`
			Source   string `json:"source"`
			Livemode bool   `json:"livemode"`
			Reason   string `json:"reason"`
		}
		if json.Unmarshal(out, &a) == nil {
			got = tapCloud{Known: true, Ready: a.Ready, Source: a.Source, Live: a.Livemode, Reason: a.Reason}
		}
	}
	c.mu.Lock()
	c.cloud, c.cloudAt = got, time.Now()
	c.mu.Unlock()
	return got
}

// tapStripeLine is the account step's line. A failed broker call is named by
// its code: transient answers (no internet, service down, anything unknown)
// are Unknown, never "not set up"; definitive refusals are not. detail is the
// raw code for "Details for support".
func tapStripeLine(c tapCloud, storeBuild bool) (key string, done, unknown bool, detail string) {
	switch {
	case !c.Known:
		code := tapNativeReason(c.ErrCode)
		key = tapReasonKey(code, "")
		switch key {
		case "tap.settings.reason.offline":
			return "tap.settings.source.unreachable", false, true, c.ErrCode
		case "tap.settings.reason.service_unavailable", "tap.settings.reason.generic":
			return key, false, true, c.ErrCode
		}
		return key, false, false, c.ErrCode
	case c.Ready && c.Source == "own":
		return "tap.settings.source.own", true, false, ""
	case c.Ready:
		return "tap.settings.source.connect", true, false, ""
	}
	switch c.Reason {
	case paybroker.CodeDeviceRoleUnrecorded:
		return "tap.settings.source.setup_again", false, false, ""
	case "needs_attention":
		return "tap.settings.source.needs_attention", false, false, ""
	case "address_incomplete":
		return "tap.settings.source.address_incomplete", false, false, ""
	case "country_unsupported":
		return "tap.settings.source.country_unsupported", false, false, ""
	}
	if storeBuild {
		return "tap.settings.source.not_configured_store", false, false, ""
	}
	return "tap.settings.source.not_configured", false, false, ""
}

// tapPermissionsGranted reports whether every permission of the grant
// allow-list is granted to the plugin (a read error is "not granted").
func tapPermissionsGranted(ctx context.Context, d *common.Deps, pluginID string) bool {
	if pluginID == "" {
		return false
	}
	for _, p := range tapGrantPermissions {
		ok, err := plugins.CheckPermissionGranted(ctx, d.Db, pluginID, p)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

// tapProviderPluginID is the one plugin id allowed to receive the grant, per provider.
var tapProviderPluginID = map[string]string{devicepay.ProviderStripe: "com.universaltill.payment-stripe"} // core-neutral:allow security: only the first-party tap plugin may be granted the broker permissions (ADR-0198 §3.1); a foreign plugin declaring the same provider must never receive them

// tapProviderPlugin is the plugin that owns provider's tap entry ("" when no
// active plugin declares one).
func tapProviderPlugin(ctx context.Context, d *common.Deps, provider string) string {
	entries, err := data.NewPluginRepo(d.Db).ListPaymentEntries(ctx)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !plugins.IsTapEntryConfig(e.ConfigJSON) {
			continue
		}
		var c struct {
			Provider string `json:"provider"`
		}
		if json.Unmarshal([]byte(e.ConfigJSON), &c) == nil && c.Provider == provider && e.PluginID == tapProviderPluginID[provider] {
			// Review R1: a manager click grants the broker and device
			// permissions; only the provider's own plugin may receive them,
			// not any plugin whose manifest merely names the provider.
			return e.PluginID
		}
	}
	return ""
}

// build reads the page's state. canGrant is whether the viewer may allow
// card taps (plugin_management); the page needs it to say so up front rather
// than offer a button the POST would refuse.
func (c *tapSettingsCtl) build(ctx context.Context, fresh, canGrant bool) tapSettingsView {
	v := tapSettingsView{Available: tapSettingsAvailable()}
	if !v.Available {
		return v
	}
	d := c.d
	v.StoreBuild = !buildinfo.CurrentChannel().AccountLinkOutAllowed()
	source := effectiveReaderSource(ctx, d, readerSourcePOS)
	v.NotMain = !tapOfferedOn(source)
	v.Additional = source == readerSourceAdditional
	caps := devicepay.Capabilities()[devicepay.ProviderStripe]
	caps.Reason = tapNativeReason(caps.Reason)
	c.mu.Lock()
	run := c.setup
	c.mu.Unlock()
	v.Running = run.running
	v.Progress = caps.Progress

	cloud := c.readCloud(ctx, fresh)
	v.Test = cloud.Known && cloud.Ready && !cloud.Live
	stripeKey, stripeDone, stripeUnknown, stripeDetail := tapStripeLine(cloud, v.StoreBuild)
	pluginID := tapProviderPlugin(ctx, d, devicepay.ProviderStripe)
	allowed := tapPermissionsGranted(ctx, d, pluginID)

	deviceBlocked := caps.Reason == "ipad" || caps.Reason == "unsupported_device" || caps.Reason == "ios_too_old"
	deviceLine := "tap.settings.step.device_ok"
	if deviceBlocked {
		deviceLine = tapReasonKey(caps.Reason, "")
	}
	if v.NotMain {
		deviceLine = "tap.settings.reason.not_main"
	}
	appleDone := caps.Ready || caps.Reason == "not_onboarded"
	appleLine := "tap.settings.apple_line"
	if !appleDone && (caps.Reason == "terms" || caps.Reason == "location") {
		appleLine = tapReasonKey(caps.Reason, "")
	}
	v.Steps = []tapStep{
		{ID: "device", Done: !deviceBlocked && !v.NotMain, LineKey: deviceLine},
		{ID: "allow", Done: allowed, LineKey: "tap.settings.permission_line"},
		{ID: "apple", Done: appleDone && !deviceBlocked, LineKey: appleLine},
		{ID: "account", Done: stripeDone, Unknown: stripeUnknown, LineKey: stripeKey, Detail: stripeDetail},
	}

	// Allow card taps comes first: it needs no network and no Apple, and Set
	// up stays visible but disabled, with its reason, until it is done. The
	// gate is guidance only: the Setup route still works without the grant.
	canSetup := !v.NotMain && !deviceBlocked && caps.Reason != "offline" && caps.Reason != "not_foreground"
	blocked := v.NotMain || deviceBlocked
	for i := range v.Steps {
		st := &v.Steps[i]
		if st.Done || blocked {
			continue
		}
		switch st.ID {
		case "allow":
			st.Action = "grant"
			switch {
			case pluginID == "":
				st.HintKey = "tap.settings.hint.plugin_missing"
			case !canGrant:
				st.HintKey = "tap.settings.reason.permission"
			}
			st.ActionOK = st.HintKey == ""
		case "apple":
			st.Action = "setup"
			st.Busy = v.Running
			if !v.Running && !allowed {
				st.HintKey = "tap.settings.hint.allow_first"
			}
			st.ActionOK = !v.Running && allowed && canSetup
		}
	}
	// The status block carries the one button that can be (or visibly cannot
	// be) pressed next: Allow while not allowed, else Set up.
	for i := range v.Steps {
		st := &v.Steps[i]
		if !st.Done && st.Action != "" && (st.ActionOK || st.HintKey != "") {
			v.Next, st.IsNext = st, true
			break
		}
	}

	allDone := true
	for _, st := range v.Steps {
		allDone = allDone && st.Done
	}
	switch {
	case v.NotMain:
		v.State, v.Icon, v.ReasonKey = "unavailable", "ℹ", "tap.settings.reason.not_main"
	case deviceBlocked, caps.Reason == "offline", caps.Reason == "not_foreground":
		v.State, v.Icon, v.ReasonKey = "unavailable", "ℹ", tapReasonKey(caps.Reason, "")
	case v.Running || caps.State == "preparing":
		v.State, v.Icon, v.ReasonKey = "preparing", "⌛", "tap.settings.setup_running"
		v.Running = true
	case allDone:
		v.State, v.Icon, v.ReasonKey = "ready", "✓", "tap.settings.lead_ready"
	default:
		v.State, v.Icon = "needs_setup", "ℹ"
		for _, s := range v.Steps {
			if s.Done {
				continue
			}
			v.ReasonKey = s.LineKey
			switch {
			case s.ID == "allow":
				v.ReasonKey = "tap.settings.lead.allow"
			case s.ID == "apple" && s.LineKey == "tap.settings.apple_line":
				v.ReasonKey = "tap.settings.lead.apple"
			}
			break
		}
	}
	if v.State == "preparing" || v.State == "unavailable" {
		// The Set up button stays in the list (disabled) while it runs.
		if v.Next != nil {
			v.Next.IsNext, v.Next = false, nil
		}
	}
	if run.done && !run.running {
		if run.res.OK {
			v.DoneNotice = true
		} else {
			v.NoticeKey = "tap.settings.setup_failed"
			v.NoticeWhy = tapReasonKey(tapNativeReason(run.res.Reason), run.res.Detail)
			v.NoticeDetail = tapDetailText(run.res.Reason, run.res.Detail)
		}
	}
	return v
}

func (c *tapSettingsCtl) renderBody(w http.ResponseWriter, r *http.Request, status int, v tapSettingsView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	httpx.RenderPartial("ui/partials/tap_settings_body.html", v)(w, r)
}

func (c *tapSettingsCtl) audit(actor, action string, payload map[string]any) {
	now := time.Now().UTC().Format(time.RFC3339)
	if err := data.NewPOSRepo(c.d.Db).InsertAudit(context.Background(), nil, actor, "tap_to_pay", devicepay.ProviderStripe, action, payload, now, ""); err != nil {
		logging.L().Errorf("tap settings audit: %s failed: %v", action, err)
	}
}

// startSetup starts the native Setup once; it reports false when one is
// already running. The run outlives the request.
func (c *tapSettingsCtl) startSetup(actor string) bool {
	c.mu.Lock()
	if c.setup.running {
		c.mu.Unlock()
		return false
	}
	c.setup = tapSetupRun{running: true}
	c.mu.Unlock()
	go func() {
		defer logging.RecoverAndLog("tap settings setup")
		ctx, cancel := context.WithTimeout(context.Background(), tapSetupTimeout)
		defer cancel()
		res := devicepay.Setup(ctx, devicepay.ProviderStripe)
		c.mu.Lock()
		c.setup = tapSetupRun{done: true, res: res}
		c.cloudAt = time.Time{} // re-read the Stripe state after a Setup
		c.mu.Unlock()
		c.audit(actor, "tap_to_pay.setup", map[string]any{"ok": res.OK, "reason": res.Reason, "detail": res.Detail})
	}()
	return true
}

func registerTapSettings(mux *http.ServeMux, d *common.Deps) {
	c := &tapSettingsCtl{d: d}

	mux.HandleFunc("GET /settings/tap-to-pay", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "settings") {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		httpx.Render("ui/pages/tap_settings.html", map[string]any{
			"title":     httpx.T(locale, "page.title.tap_settings"),
			"theme":     d.ResolvedTheme(r),
			"menuItems": d.MenuSnapshot(),
			"tap":       c.build(r.Context(), true, canPerform(d, r, "plugin_management")),
		})(w, r)
	})

	mux.HandleFunc("GET /settings/tap-to-pay/status", func(w http.ResponseWriter, r *http.Request) {
		if readerSettingsDenied(d, w, r) {
			return
		}
		c.renderBody(w, r, http.StatusOK, c.build(r.Context(), false, canPerform(d, r, "plugin_management")))
	})

	mux.HandleFunc("POST /settings/tap-to-pay/setup", func(w http.ResponseWriter, r *http.Request) {
		if readerSettingsDenied(d, w, r) {
			return
		}
		if !tapSettingsAvailable() {
			http.NotFound(w, r)
			return
		}
		if !tapOfferedOn(effectiveReaderSource(r.Context(), d, readerSourcePOS)) {
			// Only the main, standalone and additional tills take Tap to Pay
			// (ADR-0198 2, Amendment 2026-11-23): refused, no native call.
			c.renderBody(w, r, http.StatusConflict, c.build(r.Context(), false, canPerform(d, r, "plugin_management")))
			return
		}
		started := c.startSetup(settingsActorID(r))
		v := c.build(r.Context(), false, canPerform(d, r, "plugin_management"))
		v.AlreadyMsg = !started
		c.renderBody(w, r, http.StatusOK, v)
	})

	mux.HandleFunc("POST /settings/tap-to-pay/education", func(w http.ResponseWriter, r *http.Request) {
		if readerSettingsDenied(d, w, r) {
			return
		}
		if !tapSettingsAvailable() {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if res := devicepay.ShowEducation(ctx, devicepay.ProviderStripe); !res.OK {
			logging.L().Warnf("tap settings: education: %s", res.Reason)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /settings/tap-to-pay/grant", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return
		}
		pluginID := tapProviderPlugin(r.Context(), d, devicepay.ProviderStripe)
		if pluginID == "" {
			c.renderBody(w, r, http.StatusConflict, c.build(r.Context(), false, canPerform(d, r, "plugin_management")))
			return
		}
		for _, perm := range tapGrantPermissions {
			if err := plugins.GrantPermission(r.Context(), d.Db, pluginID, perm); err != nil {
				logging.L().Errorf("tap settings: grant %s: %v", perm, err)
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "common.error.server", "tap-settings", err)
				return
			}
		}
		plugins.SharedBus(d.Db).BumpGeneration()
		c.audit(settingsActorID(r), "tap_to_pay.permissions_granted", map[string]any{"plugin_id": pluginID, "permissions": tapGrantPermissions})
		c.renderBody(w, r, http.StatusOK, c.build(r.Context(), false, canPerform(d, r, "plugin_management")))
	})
}
