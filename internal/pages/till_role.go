package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Till roles (ut-docs#2781): a joined till is an "additional" till (works
// offline, rings up sales like any other) or a "satellite" (kiosk,
// table-QR or order station that needs the main till). The role is chosen
// at pairing time, can be changed later by a manager on the main till's
// Tills page, and gates this till's device profile (settings_page.go's
// POST /api/settings/display-mode). Until ut-docs#1154 a satellite has no
// other runtime behaviour — the role is a label plus that gate.
//
// The main till's tills.role column is authoritative. A joined till learns
// its own role from the admin pull (adminBundleResponse.Role, sync_admin.go)
// and keeps it in the per-till setting tillRoleSettingsKey.

// tillRoleSettingsKey is a joined till's own role, as the main till last
// reported it. sync.* is per-till (never admin-synced) and cleared by
// promote (SettingsRepo.ClearReplicaIdentity).
const tillRoleSettingsKey = "sync.till_role"

// tillRoleSyncClient is the joined till's client for reporting its own new
// role to the main till — a seam so tests can point it at an httptest
// server (same shape as userSyncProxyClient).
var tillRoleSyncClient = netaccess.NewClient(10 * time.Second)

// tillRoleFromForm validates a submitted role. Blank means
// TillRoleAdditional — every pre-#2781 sender (an older till joining, a
// form without the field) asked for an additional till.
func tillRoleFromForm(raw string) (string, bool) {
	role := strings.TrimSpace(raw)
	if role == "" {
		return data.TillRoleAdditional, true
	}
	return role, data.ValidTillRole(role)
}

// ownTillRole is THIS till's role: "" on a main or standalone till (the
// role belongs to joined tills only), otherwise what the main till last
// reported, falling back to this till's own row in the synced roster and
// then to additional.
func ownTillRole(ctx context.Context, d *common.Deps) string {
	if d.SyncPrimaryURL(ctx) == "" {
		return ""
	}
	if v, _, _ := d.Settings.Get(ctx, tillRoleSettingsKey); data.ValidTillRole(strings.TrimSpace(v)) {
		return strings.TrimSpace(v)
	}
	if id, _, _ := d.Settings.Get(ctx, "sync.till_id"); strings.TrimSpace(id) != "" {
		if role, ok, err := data.NewTillsRepo(d.Db).RoleByID(ctx, strings.TrimSpace(id)); err == nil && ok {
			return role
		}
	}
	return data.TillRoleAdditional
}

// rememberOwnTillRole stores the role the main till reported for this till
// (the admin pull), writing only on a change. An empty or unknown value —
// an older main till, or garbage — leaves the stored role alone.
func rememberOwnTillRole(ctx context.Context, d *common.Deps, role string) {
	if !data.ValidTillRole(role) {
		return
	}
	if cur, _, _ := d.Settings.Get(ctx, tillRoleSettingsKey); cur == role {
		return
	}
	if err := d.Settings.Set(ctx, tillRoleSettingsKey, role); err != nil {
		logging.L().Errorf("till role: remember own role %q: %v", role, err)
	}
}

// changeTillRole is the one role-change path (the Tills page's Change role
// and a joined till's own report): set the role and, when it really
// changed, write the plain role_changed audit row ({"from","to"}, entity
// till). ok is false for an unknown till.
func changeTillRole(ctx context.Context, tills *data.TillsRepo, posRepo *data.POSRepo, actorID, tillID, role string) (old string, ok bool, err error) {
	old, ok, err = tills.SetRole(ctx, tillID, role)
	if err != nil || !ok || old == role {
		return old, ok, err
	}
	auditRoleChange(ctx, posRepo, actorID, tillID, old, role)
	return old, true, nil
}

// auditRoleChange writes the role_changed audit row — best-effort like
// every settings audit: the change itself already happened.
func auditRoleChange(ctx context.Context, posRepo *data.POSRepo, actorID, tillID, from, to string) {
	if err := posRepo.InsertAudit(ctx, nil, actorID, "till", tillID, "role_changed",
		map[string]any{"from": from, "to": to}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
		logging.L().Errorf("till role: audit role_changed for %s failed: %v", tillID, err)
	}
}

// reportOwnRoleToMain asks the main till to set THIS joined till's role
// (POST /api/sync/till-role) — the main till's roster is authoritative, so
// a role a joined till takes on for itself (Settings → make this a
// satellite) has to land there or the next admin pull would undo it. Any
// failure is an error: the caller refuses the change rather than letting
// the two disagree.
func reportOwnRoleToMain(ctx context.Context, d *common.Deps, role string) error {
	base, bearer, ok := replicaSyncTarget(ctx, d)
	if !ok {
		return fmt.Errorf("this till has no sync bearer for its main till")
	}
	body, err := json.Marshal(map[string]string{"role": role})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/sync/till-role", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := tillRoleSyncClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("main till answered %s", resp.Status)
	}
	return nil
}

// registerTillRole wires the role-change endpoints (ut-docs#2781).
func registerTillRole(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	// The Tills page's Change role (tills_roster.html). Same manager gate
	// as every sibling on that page (sync_management), and — like revoke —
	// a main-till-authoritative write: a joined till's roster is a synced
	// copy its next admin pull would overwrite.
	mux.HandleFunc("POST /api/tills/{id}/role", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Error(w, "change a till's role on the main till", http.StatusConflict)
			return
		}
		_ = r.ParseForm()
		// No blank-means-additional default here: a role change names the
		// role it changes to.
		role := strings.TrimSpace(r.Form.Get("role"))
		if !data.ValidTillRole(role) {
			http.Error(w, "role must be additional or satellite", http.StatusBadRequest)
			return
		}
		_, found, err := changeTillRole(r.Context(), tills, posRepo, settingsActorID(r), r.PathValue("id"), role)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "till_role", err)
			return
		}
		if !found {
			http.Error(w, "till not found", http.StatusNotFound)
			return
		}
		// Same as revoke: the button refreshes only #tills-roster
		// ("ok refresh-region"); tills-changed re-fetches the nav sync chip.
		w.Header().Set("HX-Trigger", "tills-changed")
		w.WriteHeader(http.StatusNoContent)
	})

	// A joined till reports its own new role (reportOwnRoleToMain). The
	// bearer is the auth, and it can only ever change the caller's OWN row.
	mux.HandleFunc("POST /api/sync/till-role", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, tills)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
			return
		}
		var in struct {
			Role string `json:"role"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		role := strings.TrimSpace(in.Role)
		if !data.ValidTillRole(role) {
			http.Error(w, "role must be additional or satellite", http.StatusBadRequest)
			return
		}
		if _, _, err := changeTillRole(r.Context(), tills, posRepo, "system", till.ID, role); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "till_role", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"role": role}, "error": nil})
	})
}
