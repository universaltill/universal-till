package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Shop-wide settings write-through, main-till side (ut-docs#2791; same
// mechanism as ADR-0115 §1's users write-through, sync_users.go). Every
// setting that is not per-till travels main -> additional tills in the
// admin bundle (main-till-wins), so a shop-wide setting changed only on an
// additional till was silently reverted by its next pull. An additional
// till's Settings handlers now send such a change here first
// (settings_sync_proxy.go) and mirror it locally only after this answers
// 200.
//
//   - POST /api/sync/settings/apply -- {settings: [{key, value}], actor_id,
//     approver_id}. approver_id is set only when the additional till's
//     operator was PIN-elevated (checkOrElevate); then the APPROVER must
//     hold "settings", otherwise the actor must. Both are looked up in THIS
//     till's users table and decided with ITS role_permissions -- never
//     trusted from the additional till. Every key must be shop-wide by
//     data.SettingScope (per-till and unclassified keys are refused
//     not_shop_wide). The fiscal gates of /api/settings/upsert are mirrored:
//     a non-empty signing override and signing_device_failing_since are
//     never settable here; clearing an override and the two posture flags
//     need fiscal_tse_override on the ACTOR (an elevated settings approval
//     never grants it). store.country is refused (not_supported_via_sync):
//     its local fiscal-authority check and posture reset are ut-docs#2948.
//   - All keys are written in one transaction (settings.Store.SetMany),
//     each audited as setting_changed_via_till with provenance
//     {via: till-sync, till}, then this till's cached process globals are
//     re-derived (the same hook a LAN pull or cloud directive uses) and the
//     admin link is nudged so every other till pulls the change.
//   - The answer is { "data": {"settings": [...]}, "error": null } on
//     success, { "data": null, "error": {code, message} } with a 4xx on a
//     refusal.
//
// Bearer-authed via syncTill and on internal/auth/middleware.go's exempt
// list (TestSyncPullPathsAreExempt pins it). Values are never logged: a
// setting can carry something sensitive.

// syncSettingKV is one key/value pair of a write-through.
type syncSettingKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// syncSettingsApplyRequest is the POST body.
type syncSettingsApplyRequest struct {
	Settings   []syncSettingKV `json:"settings"`
	ActorID    string          `json:"actor_id"`
	ApproverID string          `json:"approver_id,omitempty"`
}

// syncSettingsApplyAnswer is the success data.
type syncSettingsApplyAnswer struct {
	Settings []syncSettingKV `json:"settings"`
}

const (
	// maxSyncSettingsBody bounds the request body.
	maxSyncSettingsBody = 256 << 10
	// maxSyncSettingsEntries bounds one batch; the Settings handlers send
	// one or two keys.
	maxSyncSettingsEntries = 50
	// maxSyncSettingKeyLen / maxSyncSettingValueLen bound one entry.
	maxSyncSettingKeyLen   = 200
	maxSyncSettingValueLen = 16 << 10
)

// registerSyncSettings mounts the main-till settings write-through endpoint
// on the bearer-authed /api/sync/* surface. refresh re-derives this till's
// cached settings globals after a successful write (pages.Init passes
// newRederiveSettings; nil in tests that don't need it).
func registerSyncSettings(mux *http.ServeMux, d *common.Deps, refresh func(context.Context)) {
	tills := data.NewTillsRepo(d.Db)
	repo := data.NewAuthRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("POST /api/sync/settings/apply", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		fail := func(status int, code, message string) {
			writeSyncOrdersJSON(w, status, nil, syncUserError{Code: code, Message: message})
		}
		till, ok := syncTill(r, tills)
		if !ok {
			fail(http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		// A till that itself follows a main till would have this change
		// reverted by its own next pull -- never accept it here.
		if d.SyncPrimaryURL(ctx) != "" {
			fail(http.StatusConflict, "replica", "this till follows a main till")
			return
		}
		var in syncSettingsApplyRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSyncSettingsBody)).Decode(&in); err != nil {
			fail(http.StatusBadRequest, "invalid_body", "invalid body")
			return
		}
		if len(in.Settings) == 0 || len(in.Settings) > maxSyncSettingsEntries {
			fail(http.StatusBadRequest, "invalid_body", "settings must hold 1 to 50 entries")
			return
		}
		in.ActorID = strings.TrimSpace(in.ActorID)
		in.ApproverID = strings.TrimSpace(in.ApproverID)

		kv := make(map[string]string, len(in.Settings))
		needsFiscalAuthority := false
		for i := range in.Settings {
			s := &in.Settings[i]
			s.Key = strings.TrimSpace(s.Key)
			if s.Key == "" || len(s.Key) > maxSyncSettingKeyLen || len(s.Value) > maxSyncSettingValueLen {
				fail(http.StatusBadRequest, "invalid_body", "key or value missing or too long")
				return
			}
			logical, storage := resolveFiscalPostureKey(d, s.Key)
			if data.SettingScope(storage) != data.SettingShopWide {
				fail(http.StatusBadRequest, "not_shop_wide", "not a shop-wide setting: "+s.Key)
				return
			}
			switch logical {
			case common.KeyCountry:
				// ut-docs#2948: the upsert handler's country change runs
				// requireFiscalAuthorityForCountryChange and resets the old
				// country's posture before persisting; not mirrored yet.
				fail(http.StatusBadRequest, "not_supported_via_sync", "store.country cannot be changed from another till yet")
				return
			case fiscal.KeyOverrideUntil, fiscal.KeyOverrideReason, fiscal.KeyOverrideActor:
				// The upsert handler's ADR-0048 gates: fabricating an
				// override is refused for everyone; clearing one is
				// owner-only.
				if s.Value != "" {
					fail(http.StatusBadRequest, "fiscal_not_settable", "fiscal override state is managed via POST /api/fiscal/signing-override")
					return
				}
				needsFiscalAuthority = true
			case wireKeySigningDeviceFailingSince:
				fail(http.StatusBadRequest, "fiscal_not_settable", "fiscal.signing_device_failing_since is not settable")
				return
			case fiscal.KeySystemOfRecord, wireKeySigningDeviceConfigured:
				needsFiscalAuthority = true
			}
			s.Key = storage
			kv[storage] = s.Value
		}

		// audit_log.actor_id FKs onto users: the actor must be a real user
		// here, which it is for any operator the admin bundle delivered.
		serverError := func(step string, err error) {
			logging.L().Errorf("sync settings from %s: %s: %v", till.Name, step, err)
			fail(http.StatusInternalServerError, "server_error", "server error")
		}
		if in.ActorID == "" {
			fail(http.StatusBadRequest, "unknown_actor", "actor_id required")
			return
		}
		actor, found, err := repo.GetUser(ctx, in.ActorID)
		if err != nil {
			serverError("actor lookup", err)
			return
		}
		if !found {
			fail(http.StatusBadRequest, "unknown_actor", "unknown actor")
			return
		}
		holder := actor
		if in.ApproverID != "" {
			approver, found, err := repo.GetUser(ctx, in.ApproverID)
			if err != nil {
				serverError("approver lookup", err)
				return
			}
			if !found {
				fail(http.StatusBadRequest, "unknown_approver", "unknown approver")
				return
			}
			holder = approver
		}
		// may reports whether u is active and its role holds perm on THIS
		// till.
		may := func(u data.UserRow, perm string) (bool, error) {
			if !u.IsActive {
				return false, nil
			}
			return repo.HasPermission(ctx, u.Role, perm)
		}
		forbidden := func(who data.UserRow, perm string) {
			logging.L().Infof("sync settings: change by %s (%s) from %s refused — no %s on the main till", who.ID, who.Role, till.Name, perm)
			fail(http.StatusForbidden, "forbidden", "actor may not make this change")
		}
		if ok, err := may(holder, "settings"); err != nil {
			serverError("permission check", err)
			return
		} else if !ok {
			forbidden(holder, "settings")
			return
		}
		// The upsert handler checks fiscal_tse_override against the SESSION
		// user, never the elevation's approver: mirrored with the actor.
		// The actor must also be active here, whoever approved.
		if needsFiscalAuthority {
			if ok, err := may(actor, "fiscal_tse_override"); err != nil {
				serverError("permission check", err)
				return
			} else if !ok {
				forbidden(actor, "fiscal_tse_override")
				return
			}
		}

		if err := d.Settings.SetMany(ctx, kv); err != nil {
			serverError("write", err)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		for _, s := range in.Settings {
			payload := map[string]any{"via": "till-sync", "till": till.Name, "value": s.Value}
			var err error
			if in.ApproverID != "" {
				err = posRepo.InsertAuditElevated(ctx, nil, in.ApproverID, in.ActorID, "settings", s.Key, "setting_changed_via_till", payload, now, "")
			} else {
				err = posRepo.InsertAudit(ctx, nil, in.ActorID, "settings", s.Key, "setting_changed_via_till", payload, now, "")
			}
			if err != nil {
				// Best-effort like settingsAudit: the write already
				// succeeded.
				logging.L().Errorf("sync settings: audit of %s from %s failed: %v", s.Key, till.Name, err)
			}
		}
		if refresh != nil {
			refresh(ctx)
		}
		d.NudgeLink(fleetlink.ScopeAdmin)
		keys := make([]string, 0, len(in.Settings))
		for _, s := range in.Settings {
			keys = append(keys, s.Key)
		}
		logging.L().Infof("sync settings: %s applied from %s by %s (ut-docs#2791)", strings.Join(keys, ", "), till.Name, holder.ID)
		writeSyncOrdersJSON(w, http.StatusOK, syncSettingsApplyAnswer{Settings: in.Settings}, nil)
	})
}
