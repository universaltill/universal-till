package pages

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/tills"
)

// Old entries (ut-docs#7173): the tills the shop account still lists that are
// not set up on this till (or that this till lists and the account no longer
// does, or has not seen for a long time). One list, one Remove per entry, and
// a "Remove all" for the lot. The reads are the existing tills.Merge view; the
// local removals post to the EXISTING revoke route; the account-side removal
// of a cloud-only entry goes through the removeFromAccount seam below.

// Kinds of old entry. local and old are removed with the existing
// POST /api/sync/tills/{id}/revoke; cloud with POST .../remove-from-account.
const (
	oldKindLocal = "local" // enrolled on this network, absent from the account
	oldKindOld   = "old"   // same, and unseen for a long time or removed by the account
	oldKindCloud = "cloud" // in the account, never added on this network
)

// oldEntry is one row of the Old entries list.
type oldEntry struct {
	ID, Name, Kind, LastSeen string
	LongUnseen               bool
	// SameAsID / SameAsName: a live till that might be the same phone (a
	// hint, never a fact: the UI says "might be").
	SameAsID, SameAsName string
	// Removable: the Remove button is offered. False only for a cloud-only
	// entry while account removal is not available yet.
	Removable bool
}

// oldEntriesFrom lists every ghost of the merged view, cloud-only ones last.
func oldEntriesFrom(o tillsOthers) []oldEntry {
	var out []oldEntry
	add := func(r tills.Row, kind string) {
		e := oldEntry{ID: r.ID, Name: r.Name, Kind: kind, LastSeen: r.LastSeen, LongUnseen: r.LongUnseen}
		if live := sameDeviceHint(r, o.Rows); live != nil {
			e.SameAsID, e.SameAsName = live.ID, live.Name
		}
		e.Removable = kind != oldKindCloud || accountRemovalReady
		out = append(out, e)
	}
	for _, r := range o.Stale {
		if r.State == tills.StateOldEntry {
			add(r, oldKindOld)
		} else {
			add(r, oldKindLocal)
		}
	}
	for _, r := range o.CloudOnly {
		add(r, oldKindCloud)
	}
	return out
}

// sameDeviceHint is the seam for "this is the same phone as ...". Today it is
// a heuristic: the live till (not this one, not a ghost) with a different
// name that was seen most recently AFTER the ghost. epic-pairing's
// register_devices link (device id equality, #7098) is the authoritative
// answer and replaces this body when it lands.
var sameDeviceHint = func(ghost tills.Row, rows []tills.Row) *tills.Row {
	gt, ok := parseSeen(ghost.LastSeen)
	if !ok {
		return nil
	}
	var best *tills.Row
	var bestT time.Time
	for i := range rows {
		r := rows[i]
		if r.IsSelf || r.State != tills.StateOK || !r.InLocal || strings.EqualFold(r.Name, ghost.Name) {
			continue
		}
		t, ok := parseSeen(r.LastSeen)
		if !ok || !t.After(gt) {
			continue
		}
		if best == nil || t.After(bestT) {
			best, bestT = &rows[i], t
		}
	}
	return best
}

func parseSeen(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// errAccountRemoveUnavailable: the cloud has no removal call yet.
var errAccountRemoveUnavailable = errors.New("tills: removing a till from the account is not available yet")

// accountRemovalReady is false until epic-pairing (#7098) ships the account
// removal (enroll.RemoveFleetDevice + the ut-cloud endpoint). While false the
// cloud-only entries say so honestly ("arrives in the next update") and carry
// no Remove button. The swap is: set it to true and point removeFromAccount
// at the enroll call; nothing else in this card changes.
var accountRemovalReady = false

// removeFromAccount removes one till from the shop account (the cloud).
var removeFromAccount = func(ctx context.Context, d *common.Deps, tillID string) error {
	return errAccountRemoveUnavailable
}

// oldBanner is what the Other tills banner says about the old entries.
type oldBanner struct {
	Count     int    // old entries in total
	Removable int    // of those, how many the button removes now
	Names     string // quoted names, "a" and "b"
	Phone     string // the live till they probably were, "" when unknown
	Pending   bool   // some entries wait for account removal (the seam is a stub)
}

func oldBannerFrom(entries []oldEntry, locale string, and string) oldBanner {
	b := oldBanner{Count: len(entries)}
	var names []string
	for _, e := range entries {
		names = append(names, "“"+e.Name+"”")
		if e.Removable {
			b.Removable++
		} else {
			b.Pending = true
		}
		if b.Phone == "" {
			b.Phone = e.SameAsName
		}
	}
	switch len(names) {
	case 0:
	case 1:
		b.Names = names[0]
	default:
		b.Names = strings.Join(names[:len(names)-1], ", ") + " " + and + " " + names[len(names)-1]
	}
	return b
}

// oldEntriesCountCached is the Old entries tag in the tree: from the local
// roster and the cached account list only. 0 when the account list is not
// known (never a fetch, never "empty means fine").
func oldEntriesCountCached(ctx context.Context, d *common.Deps) int {
	if d == nil || d.Db == nil {
		return 0
	}
	o, err := tillsOthersView(ctx, d, map[string]any{}, false, false)
	if err != nil || !o.CloudOK {
		return 0
	}
	return len(o.Stale) + len(o.CloudOnly)
}

// verifyRemovalPIN is the manager-PIN step of every removal (owner, 2026-11-27:
// the PIN is asked inline in the row, no dialog). It is checkStepUp: the
// session must already hold sync_management AND a fresh manager PIN is
// required, so a person at an unlocked till cannot remove an entry without
// it. A false return has already written the response.
func verifyRemovalPIN(d *common.Deps, w http.ResponseWriter, r *http.Request) bool {
	_ = r.ParseForm()
	elev := checkStepUp(d, r, "sync_management", strings.TrimSpace(r.FormValue("override_pin")))
	if elev.Outcome == elevated {
		return true
	}
	key := elevationErrorKey(elev.Err)
	status := http.StatusForbidden
	switch {
	case elev.Err == nil: // no PIN typed
		key = "elevation.error_invalid_pin"
		status = http.StatusUnauthorized
	case errors.Is(elev.Err, auth.ErrInvalidPIN):
		status = http.StatusUnauthorized
	case errors.Is(elev.Err, auth.ErrLockedOut):
		status = http.StatusTooManyRequests
	}
	common.LocalizedError(w, r, status, key)
	return false
}

// registerTillsOldEntries wires the removal routes of the Old entries section:
// POST /api/sync/tills/verify-pin (the inline PIN check the local revoke
// waits for) and POST /api/sync/tills/{id}/remove-from-account.
//
// Every removal route checks the PIN itself (verifyRemovalPIN): the revoke in
// sync_api.go and remove-from-account here. verify-pin only lets "Remove all"
// fail early on a wrong PIN, before any entry is removed; it is not the gate.
func registerTillsOldEntries(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
	mux.HandleFunc("POST /api/sync/tills/verify-pin", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		if verifyRemovalPIN(d, w, r) {
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /api/sync/tills/{id}/remove-from-account", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		if d.SyncPrimaryURL(r.Context()) != "" {
			common.LocalizedError(w, r, http.StatusConflict, "tills.tree.old.error.main_only")
			return
		}
		id := r.PathValue("id")
		if id == "" || len(id) > 200 || id == "verify-pin" {
			http.NotFound(w, r)
			return
		}
		if !accountRemovalReady {
			common.LocalizedError(w, r, http.StatusNotImplemented, "tills.tree.old.cloud_pending")
			return
		}
		if !verifyRemovalPIN(d, w, r) {
			return
		}
		// Only an entry the account really lists, read right now.
		devs, ok := accountFleet(r.Context(), d, true)
		if !ok {
			common.LocalizedError(w, r, http.StatusConflict, "tills.list.error.account_unavailable")
			return
		}
		found := false
		for _, dev := range devs {
			if dev.TillID == id {
				found = true
				break
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		if err := removeFromAccount(r.Context(), d, id); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusBadGateway, "tills.tree.old.error.account", "tills_remove_from_account", err)
			return
		}
		fleetCache.mu.Lock()
		fleetCache.at = time.Time{}
		fleetCache.mu.Unlock()
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "till", id, "till_removed_from_account",
			nil, time.Now().UTC().Format(time.RFC3339), "")
		w.Header().Set("HX-Trigger", "tills-changed")
		w.WriteHeader(http.StatusNoContent)
	})
}

// RemoveURL is the route that removes this entry: the existing revoke for an
// entry this network holds, the account-side removal for a cloud-only one.
func (e oldEntry) RemoveURL() string {
	if e.Kind == oldKindCloud {
		return "/api/sync/tills/" + e.ID + "/remove-from-account"
	}
	return "/api/sync/tills/" + e.ID + "/revoke"
}

// oldRemoveURLs is every removable entry's route, space separated, for the one
// "Remove all" button (tills-tree.js posts them one by one, so each local
// removal is exactly the existing revoke).
func oldRemoveURLs(entries []oldEntry) string {
	var urls []string
	for _, e := range entries {
		if e.Removable {
			urls = append(urls, e.RemoveURL())
		}
	}
	return strings.Join(urls, " ")
}
