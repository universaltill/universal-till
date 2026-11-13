package pages

import (
	"context"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5145: the cloud refused to re-register this till (409 device_live)
// because it was in use in the last 72 h. The till keeps selling (ADR-0003);
// the lazy path waits for usable_from (enroll.RegisterBlockedUntil), the chip
// (cloud_auth_chip.go) and Settings > Till registration say when it can
// reconnect, and a manager gets the two facts support needs to lift it sooner:
// the device ID and the approval key fingerprint.

// tillKeyFingerprint is this till's approval key fingerprint; a var so a test
// can make the key unreadable.
var tillKeyFingerprint = func() (string, error) { return approvalkey.Shared().Fingerprint() }

// clearAuthLockout ends the 401 lock-out after a Reconnect or a new-shop
// registration; a var so a test can observe it.
var clearAuthLockout = cloudsync.ClearAuthLockout

// registerBlocked is the device_live block in force: on an unregistered till,
// or on a registered one that is locked out (a refused Reconnect sets it, and a
// healthy registered till never shows a stale one).
func registerBlocked(ctx context.Context, d *common.Deps) (time.Time, bool) {
	if d == nil || d.Settings == nil {
		return time.Time{}, false
	}
	s := enroll.CurrentStatus()
	if s.ViaMainTill || (s.Registered && !cloudAuthChipStatus().Show) {
		return time.Time{}, false
	}
	until := enroll.RegisterBlockedUntil(ctx, d.Settings)
	return until, !until.IsZero()
}

// registerKeyRefused: the cloud refused this unregistered till's key proof; the
// lazy path is quiet until an operator action.
func registerKeyRefused(ctx context.Context, d *common.Deps) bool {
	if d == nil || d.Settings == nil {
		return false
	}
	if s := enroll.CurrentStatus(); s.Registered || s.ViaMainTill {
		return false
	}
	return !enroll.KeyRefusedAt(ctx, d.Settings).IsZero()
}

// blockedWhen is the moment the till can reconnect, in the locale's date
// convention and the till's own time zone, isolated so it keeps its order
// inside a right-to-left sentence (U+2068 .. U+2069, what <bdi> does in HTML).
func blockedWhen(locale string, t time.Time) string {
	return "⁨" + httpx.FormatDateTime(t.Local(), locale) + "⁩"
}

// blockedHintText is the manager's chip title: when, and that support can lift it.
func blockedHintText(locale string, until time.Time) string {
	return fmt.Sprintf(httpx.T(locale, "status.cloud_auth.blocked_hint"), blockedWhen(locale, until))
}

// deviceLiveText is "Register now"'s answer for a device_live refusal, as
// HTML-safe text with the date in a <bdi>. The raw cloud body is never used.
func deviceLiveText(locale string, usableFrom time.Time) string {
	tmpl := html.EscapeString(httpx.T(locale, "settings.enrol.device_live"))
	when := "…"
	if !usableFrom.IsZero() && usableFrom.After(time.Now()) {
		when = html.EscapeString(httpx.FormatDateTime(usableFrom.Local(), locale))
	} else {
		return html.EscapeString(httpx.T(locale, "settings.till_registration.blocked_soon"))
	}
	return strings.Replace(tmpl, "%s", "<bdi>"+when+"</bdi>", 1)
}

// enrolFailureHTML is the HTML-safe, translated message for a Register now /
// Pair failure that is not a network problem, or "" for the generic text. It
// adds the device_live refusal (which carries a date) to enrolFailureKey.
func enrolFailureHTML(locale string, err error) string {
	if usable, ok := enroll.IsDeviceLive(err); ok {
		return deviceLiveText(locale, usable)
	}
	switch {
	case enroll.IsKeyRefused(err):
		return html.EscapeString(httpx.T(locale, "settings.till_registration.key_refused"))
	case enroll.IsUpdateNeeded(err):
		return html.EscapeString(httpx.T(locale, "settings.enrol.update_needed"))
	case enroll.IsRateLimited(err):
		return html.EscapeString(httpx.T(locale, "settings.enrol.rate_limited"))
	case enroll.IsStoreNotReusable(err):
		return html.EscapeString(httpx.T(locale, "settings.till_registration.store_not_reusable"))
	}
	if key := enrolFailureKey(err); key != "" {
		return html.EscapeString(httpx.T(locale, key))
	}
	return ""
}

// reconnectResultHTML is the result slot text of Reconnect: translated,
// HTML-safe, never the raw cloud body. Every refusal leaves the till as it was.
func reconnectResultHTML(locale string, err error) string {
	if usable, ok := enroll.IsDeviceLive(err); ok {
		return deviceLiveText(locale, usable) + " " + html.EscapeString(httpx.T(locale, "settings.till_registration.blocked_help"))
	}
	if s := enrolFailureHTML(locale, err); s != "" {
		return s
	}
	if errors.Is(err, enroll.ErrReconnectUnavailable) || errors.Is(err, enroll.ErrPairIdentityPinned) {
		return html.EscapeString(httpx.T(locale, "settings.till_registration.reconnect_unavailable"))
	}
	return html.EscapeString(httpx.T(locale, "settings.till_registration.reconnect_failed"))
}

// registerBlockedView is the Settings > Till registration notice, nil when the
// till is neither blocked nor refused. Everyone who can open Settings sees the
// notice (it is the till's state); the device ID and key fingerprint are in
// registerFactsView, for a manager only.
func registerBlockedView(r *http.Request, d *common.Deps, locale string) map[string]any {
	if until, ok := registerBlocked(r.Context(), d); ok {
		return map[string]any{
			"help": httpx.T(locale, "settings.till_registration.blocked_help"),
			"notice": template.HTML(strings.Replace( //nolint:gosec // the template text is escaped; the date is escaped
				html.EscapeString(httpx.T(locale, "settings.till_registration.blocked_until")), "%s",
				"<bdi>"+html.EscapeString(httpx.FormatDateTime(until.Local(), locale))+"</bdi>", 1)),
		}
	}
	if registerKeyRefused(r.Context(), d) {
		return map[string]any{
			"help":   httpx.T(locale, "settings.till_registration.blocked_help"),
			"notice": httpx.T(locale, "settings.till_registration.key_refused"),
		}
	}
	return nil
}

// registerFactsView is the device ID and key fingerprint support asks for,
// for a viewer who may manage Settings, on a main or standalone till in every
// state (registered, not registered, blocked, locked out); nil otherwise. A
// cashier never gets it. Both are public identifiers, not secrets.
func registerFactsView(r *http.Request, d *common.Deps, locale string) map[string]any {
	if d == nil || !canPerform(d, r, "settings") || enroll.CurrentStatus().ViaMainTill {
		return nil
	}
	v := map[string]any{"deviceID": enroll.CurrentStatus().DeviceID}
	if fp, err := tillKeyFingerprint(); err == nil && fp != "" {
		v["fingerprint"] = approvalkey.FingerprintGroups(fp)
	} else {
		// The reason stays in the log (approvalkey already logged it); the
		// operator sees the translated line.
		v["fingerprintUnavailable"] = httpx.T(locale, "settings.till_registration.fingerprint_unavailable")
	}
	return v
}

// reconnectPanelView is the lock-out panel of a registered till of an ownerless
// shop whose cloud sign-in was refused (the chip's "register" variant), for a
// viewer who may manage Settings; nil otherwise. A claimed store is paired
// again instead (the chip says "repair"), a replica or an environment-pinned
// identity cannot reconnect here.
func reconnectPanelView(r *http.Request, d *common.Deps) map[string]any {
	if d == nil || d.Settings == nil || !canPerform(d, r, "settings") || !enroll.CurrentStatus().Registered {
		return nil
	}
	if !tillLockedOutAnonymous(r.Context(), d) {
		return nil
	}
	return map[string]any{"keyRefused": !enroll.KeyRefusedAt(r.Context(), d.Settings).IsZero()}
}

// tillLockedOutAnonymous is the one state in which Reconnect / "Register as a
// new shop" apply (ADR-0116 D6, ut-docs#5145): a Registered main or standalone
// till (not a replica, not env-pinned) whose cloud sign-in was lost on a store
// that has no owner. The panel and POST /api/enrol/new-shop share it, so a
// hidden button is also refused server-side (v2 L1).
func tillLockedOutAnonymous(ctx context.Context, d *common.Deps) bool {
	if d == nil || d.Settings == nil || !enroll.CurrentStatus().Registered || !enroll.CanReconnect(ctx, d.Settings) {
		return false
	}
	return cloudAuthChipVariant(cloudAuthChipStatus(), storeHasOwner(ctx, d.Settings)) == cloudAuthRegister
}
