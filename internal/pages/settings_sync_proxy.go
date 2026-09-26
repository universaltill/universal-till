package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"sort"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Shop-wide settings write-through, additional-till side (ut-docs#2791).
// The Settings handlers in settings_page.go that write a key with a plain
// store write call saveShopSettings instead. On a till that follows a main
// till, every key that is not per-till (data.SettingScope) is sent to the
// main till's /api/sync/settings/apply (sync_settings.go) instead of being
// written locally -- the admin bundle is main-till-wins, so a local-only
// write would be silently overwritten by the next pull.
//
// Main till answers 200 -> the answered keys are mirrored locally so the
// change is visible at once (the next pull writes the same values). ANY
// failure -- unreachable, timeout, non-200, bad body, no bearer yet --
// refuses the change with NO local write, exactly like the users
// write-through (user_sync_proxy.go) and for the same reason. Per-till keys
// in the same call are written locally only after the main till accepted
// the shop-wide ones. Values are never logged. An admin screen; checkout
// never depends on it (offline-first).
//
// The handlers that persist through common.SaveState (the store card, the
// kiosk and sell-screen policies, the per-till display cards) call
// saveStateThrough, which sends only the fields the handler changed
// (ut-docs#2948). Not covered yet: store.country from another till
// (ut-docs#2980), shop-wide writes made outside these handlers
// (ut-docs#2979) and read-only rendering while the main till is away
// (ut-docs#2981).

// settingsSyncProxyClient is the additional-till -> main-till client; same
// admin-screen budget as the users write-through.
var settingsSyncProxyClient = &http.Client{Timeout: userSyncTimeout}

// errSettingsSync is a failed write-through. Code is the main till's
// refusal code on a 4xx answer; "" means the main till could not be
// reached or its answer could not be used.
type errSettingsSync struct {
	Status int
	Code   string
}

func (e *errSettingsSync) Error() string {
	if e.Code == "" {
		return "main till unreachable"
	}
	return "main till refused the change: " + e.Code
}

// settingsSyncFailure returns the write-through failure inside err, if any.
func settingsSyncFailure(err error) (*errSettingsSync, bool) {
	var se *errSettingsSync
	ok := errors.As(err, &se)
	return se, ok
}

// settingsSyncMessage is the translated message for a failed write-through.
func settingsSyncMessage(r *http.Request, w http.ResponseWriter, se *errSettingsSync) string {
	key := "settings.error.main_till_unreachable"
	switch se.Code {
	case "":
	case "not_supported_via_sync":
		key = "settings.error.change_on_main_till"
	default:
		key = "settings.error.main_till_refused"
	}
	return httpx.T(httpx.ResolveLocale(w, r), key)
}

// respondSettingsSyncFragment answers a failed write-through on a handler
// whose error shape is an inline `<span class="error">` fragment (200, so
// htmx swaps it): 403 for a main-till permission refusal, like the local
// one. It returns false when err is not a write-through failure, so the
// caller keeps its own error shape for a local store error.
func respondSettingsSyncFragment(w http.ResponseWriter, r *http.Request, err error) bool {
	se, ok := settingsSyncFailure(err)
	if !ok {
		return false
	}
	if se.Code == "forbidden" {
		http.Error(w, httpx.T(httpx.ResolveLocale(w, r), "settings.error.main_till_refused"), http.StatusForbidden)
		return true
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<span class="error">✗ %s</span>`, html.EscapeString(settingsSyncMessage(r, w, se)))
	return true
}

// respondSettingsSyncError answers a failed write-through on a handler
// whose error shape is an http.Error text body (the upsert card renders
// it): 403 forbidden, 409 for any other main-till refusal, 502 when the
// main till could not be reached. False when err is not a write-through
// failure.
func respondSettingsSyncError(w http.ResponseWriter, r *http.Request, err error) bool {
	se, ok := settingsSyncFailure(err)
	if !ok {
		return false
	}
	switch {
	case se.Code == "forbidden":
		http.Error(w, httpx.T(httpx.ResolveLocale(w, r), "settings.error.main_till_refused"), http.StatusForbidden)
	case se.Code != "":
		http.Error(w, settingsSyncMessage(r, w, se), http.StatusConflict)
	default:
		http.Error(w, settingsSyncMessage(r, w, se), http.StatusBadGateway)
	}
	return true
}

// saveShopSettings persists kv the way this till must: locally on a main
// till (or for per-till keys), through the main till for everything else
// on a till that follows one. elev is the handler's checkOrElevate result:
// its actor, and its approver when the operator was PIN-elevated, travel so
// the main till can decide with its own roles. A failed write-through is an
// *errSettingsSync; anything else is a local store error.
func saveShopSettings(ctx context.Context, d *common.Deps, elev elevationCheck, kv map[string]string) error {
	local := map[string]string{}
	var shop []syncSettingKV
	follows := tillFollowsMain(ctx, d)
	for k, v := range kv {
		if follows && data.SettingScope(k) != data.SettingPerTill {
			shop = append(shop, syncSettingKV{Key: k, Value: v})
		} else {
			local[k] = v
		}
	}
	if len(shop) > 0 {
		sort.Slice(shop, func(i, j int) bool { return shop[i].Key < shop[j].Key })
		in := syncSettingsApplyRequest{Settings: shop, ActorID: elev.ActorID}
		if elev.Outcome == elevated {
			in.ApproverID = elev.ApproverID
		}
		answered, err := applySettingsOnMain(ctx, d, settingsSyncProxyClient, in)
		if err != nil {
			return err
		}
		mirror := make(map[string]string, len(answered))
		for _, s := range answered {
			mirror[s.Key] = s.Value
		}
		// Best-effort: the main till holds the change and the next pull
		// lands it here anyway.
		if err := d.Settings.SetMany(ctx, mirror); err != nil {
			logging.L().Errorf("settings sync: local mirror failed (the main till holds the change; the next pull lands it): %v", err)
		}
	}
	switch len(local) {
	case 0:
		return nil
	case 1:
		for k, v := range local {
			return d.Settings.Set(ctx, k, v)
		}
	}
	return d.Settings.SetMany(ctx, local)
}

// applySettingsOnMain POSTs one batch to the main till and returns the
// answered settings, or an *errSettingsSync. Never logs the body or any
// value.
func applySettingsOnMain(ctx context.Context, d *common.Deps, client *http.Client, in syncSettingsApplyRequest) ([]syncSettingKV, error) {
	unreachable := &errSettingsSync{}
	keys := make([]string, 0, len(in.Settings))
	for _, s := range in.Settings {
		keys = append(keys, s.Key)
	}
	base, bearer, ok := replicaSyncTarget(ctx, d)
	if !ok {
		logging.L().Infof("settings sync: %v refused — this till has no sync bearer for its main till yet", keys)
		return nil, unreachable
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return nil, unreachable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/sync/settings/apply", bytes.NewReader(payload))
	if err != nil {
		return nil, unreachable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Infof("settings sync: main till unreachable on %v (%v) — change refused", keys, err)
		return nil, unreachable
	}
	defer resp.Body.Close()
	var out struct {
		Data  *syncSettingsApplyAnswer `json:"data"`
		Error *syncUserError           `json:"error"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		se := &errSettingsSync{Status: resp.StatusCode}
		// Only a 4xx is the main till's decision about this change; a 5xx
		// or a proxy's page is "couldn't get an answer".
		if decodeErr == nil && out.Error != nil && resp.StatusCode < 500 {
			se.Code = out.Error.Code
		}
		logging.L().Infof("settings sync: main till answered %s on %v (code %q) — change refused", resp.Status, keys, se.Code)
		return nil, se
	}
	if decodeErr != nil || out.Data == nil || len(out.Data.Settings) == 0 {
		logging.L().Infof("settings sync: unusable main-till answer on %v — change refused", keys)
		return nil, unreachable
	}
	return out.Data.Settings, nil
}

// saveStateThrough persists st the way this till must (ut-docs#2948). On a
// main till it is common.SaveState, then extra best-effort -- what the
// handlers did before. On a till that follows one, SaveState's
// every-key rewrite would send this till's copy of every shop-wide setting
// (possibly stale) over the main till's, so only the shop-wide rows st
// changed against base travel -- the snapshot the handler edited, never a
// fresh d.CurrentState() that a concurrent admin pull may already have
// replaced (#2948 review finding 1) -- together with extra (the keys
// the change implies, e.g. store.currency_confirmed) in the same batch;
// per-till rows are written locally as SaveState writes them, after the
// main till accepted. A failed write-through is an *errSettingsSync and
// writes nothing.
func saveStateThrough(ctx context.Context, d *common.Deps, elev elevationCheck, base, st common.RuntimeState, extra map[string]string) error {
	if !tillFollowsMain(ctx, d) {
		if err := common.SaveState(ctx, d.Settings, st); err != nil {
			return err
		}
		if len(extra) > 0 {
			if err := d.Settings.SetMany(ctx, extra); err != nil {
				logging.L().Errorf("settings: write implied keys: %v", err)
			}
		}
		return nil
	}
	prev := common.StateKV(ctx, d.Settings, base)
	kv := make(map[string]string, len(extra))
	for k, v := range common.StateKV(ctx, d.Settings, st) {
		if data.SettingScope(k) == data.SettingPerTill {
			kv[k] = v
			continue
		}
		if old, ok := prev[k]; !ok || old != v {
			kv[k] = v
		}
	}
	for k, v := range extra {
		kv[k] = v
	}
	return saveShopSettings(ctx, d, elev, kv)
}
