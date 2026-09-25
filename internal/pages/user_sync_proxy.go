package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Users and PINs write-through, additional-till side (ADR-0115 §1,
// ut-docs#2755). users_page.go's create / PIN / active / role /
// promote-super-admin handlers and auth_page.go's own-PIN change keep every
// local validation and permission check they had, then -- on a till that
// follows a main till -- send the change to the main till's
// /api/sync/users/apply (sync_users.go) instead of writing locally. The PIN
// is hashed HERE and only the hash travels (the LAN link is not TLS yet;
// the hash already reaches every till in the admin bundle).
//
// Main till answers 200 -> the answered row (plus the hash this till sent)
// is mirrored into the local users table so the change is visible at once;
// the next admin-bundle pull writes the same values. ANY failure --
// unreachable, timeout, non-200, bad body, no bearer yet -- refuses the
// change with NO local write. This deliberately differs from ADR-0093's
// held-sale local fallback: a local-only user edit is guaranteed to be
// reverted by the next pull, so a fallback would only hide the loss. This
// is an admin screen; checkout never depends on it (offline-first).

// userSyncTimeout bounds one write-through: an admin-only screen, not the
// checkout path (heldSaleProxyClient's 800ms), so it can wait out a busy LAN.
const userSyncTimeout = 5 * time.Second

// userSyncProxyClient is the additional-till -> main-till client.
var userSyncProxyClient = &http.Client{Timeout: userSyncTimeout}

// errUserSync is a failed write-through. Code is the main till's refusal
// code on a 4xx answer; "" means the main till could not be reached or its
// answer could not be used.
type errUserSync struct {
	Status int
	Code   string
}

func (e *errUserSync) Error() string {
	if e.Code == "" {
		return "main till unreachable"
	}
	return "main till refused the change: " + e.Code
}

// userSyncRefusalKeys maps a main-till refusal code to the existing
// users-page error key an operator already knows for it. Anything else is
// the generic "can't reach the main till" message.
var userSyncRefusalKeys = map[string]string{
	"username_taken":   "users.error.create",
	"last_admin":       "users.error.last_admin",
	"last_super_admin": "users.error.last_super_admin",
	"invalid_role":     "users.error.role",
	"required":         "users.error.required",
}

// userSyncForbidden reports whether the main till refused the change
// because the actor may not make it (its "forbidden" code). Callers answer
// that with a 403, exactly as their own local permission refusals do,
// rather than an inline message.
func userSyncForbidden(err error) bool {
	var se *errUserSync
	return errors.As(err, &se) && se.Code == "forbidden"
}

// respondUserSyncError answers a failed write-through on the users page:
// 403 for a main-till permission refusal, the mapped inline message for
// anything else.
func respondUserSyncError(w http.ResponseWriter, r *http.Request, err error) {
	if userSyncForbidden(err) {
		http.Error(w, "forbidden by the main till", http.StatusForbidden)
		return
	}
	usersRespondError(w, r, userSyncErrorKey(err))
}

// userSyncErrorKey is the translated error key to show for a failed
// write-through.
func userSyncErrorKey(err error) string {
	var se *errUserSync
	if errors.As(err, &se) {
		if key, ok := userSyncRefusalKeys[se.Code]; ok {
			return key
		}
	}
	return "users.error.main_till_unreachable"
}

// tillFollowsMain reports whether user changes on this till must go
// through the main till -- the same "sync.primary_url is set" test the
// catalog pages' requirePrimary uses. A till with the URL but no bearer
// yet still follows a main till: its changes are refused, not kept locally.
func tillFollowsMain(ctx context.Context, d *common.Deps) bool {
	return d.SyncPrimaryURL(ctx) != ""
}

// applyUserOnMain POSTs one change to the main till. On success it returns
// the answered row; on any failure an *errUserSync. Never logs the body:
// it can carry a PIN hash.
func applyUserOnMain(ctx context.Context, d *common.Deps, client *http.Client, in syncUserApplyRequest) (syncUserRow, error) {
	unreachable := &errUserSync{}
	base, bearer, ok := replicaSyncTarget(ctx, d)
	if !ok {
		logging.L().Infof("user sync: %s refused — this till has no sync bearer for its main till yet", in.Op)
		return syncUserRow{}, unreachable
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return syncUserRow{}, unreachable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/sync/users/apply", bytes.NewReader(payload))
	if err != nil {
		return syncUserRow{}, unreachable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Infof("user sync: main till unreachable on %s (%v) — change refused", in.Op, err)
		return syncUserRow{}, unreachable
	}
	defer resp.Body.Close()
	var out struct {
		Data  *syncUserRow   `json:"data"`
		Error *syncUserError `json:"error"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		se := &errUserSync{Status: resp.StatusCode}
		// Only a 4xx is the main till's decision about this change; a 5xx
		// or a proxy's page is "couldn't get an answer".
		if decodeErr == nil && out.Error != nil && resp.StatusCode < 500 {
			se.Code = out.Error.Code
		}
		logging.L().Infof("user sync: main till answered %s on %s (code %q) — change refused", resp.Status, in.Op, se.Code)
		return syncUserRow{}, se
	}
	if decodeErr != nil || out.Data == nil || out.Data.ID == "" {
		logging.L().Infof("user sync: unusable main-till answer on %s — change refused", in.Op)
		return syncUserRow{}, unreachable
	}
	return *out.Data, nil
}

// userWriteThrough sends one change to the main till and, once it answered
// 200, mirrors the answered row into the local users table, with the hash
// this till sent when the change carried one. The mirror is best-effort (a
// failure is logged; the main till holds the change and the next pull
// lands it) -- the main till's answer is what makes the change real.
func userWriteThrough(ctx context.Context, d *common.Deps, repo *data.AuthRepo, in syncUserApplyRequest) (data.UserRow, error) {
	row, err := applyUserOnMain(ctx, d, userSyncProxyClient, in)
	if err != nil {
		return data.UserRow{}, err
	}
	u := data.UserRow{
		ID:          row.ID,
		Username:    row.Username,
		DisplayName: row.DisplayName,
		Role:        row.Role,
		PinHash:     in.PinHash,
		IsActive:    row.Active,
	}
	if err := repo.MirrorUser(ctx, u); err != nil {
		logging.L().Errorf("user sync: local mirror of user %s failed (the main till holds the change; the next pull lands it): %v", u.ID, err)
	}
	return u, nil
}
