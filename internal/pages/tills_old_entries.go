package pages

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/tills"
)

// Old entries (ut-docs#7173): the tills the shop account and this till
// disagree about and that a manager can tidy now: an entry this till lists that
// the account no longer does or that has not been seen for a long time, and an
// entry only the shop's online list holds. One list, one Remove per entry, and
// a "Remove all" for the lot. The reads are the tills.Merge view; every
// removal posts to main's routes (ut-docs#7098): POST /api/sync/tills/{id}/revoke
// for an entry this network holds, POST /api/sync/tills/cloud-remove for an
// account-only one. Both enforce the one manager-PIN rule
// (authorizeTillRemoval).

// Kinds of old entry. local and old are removed with the revoke route; cloud
// with the cloud-remove route.
const (
	oldKindLocal = "local" // enrolled on this network, absent from the account
	oldKindOld   = "old"   // same, and unseen for a long time or removed by the account
	oldKindCloud = "cloud" // in the account, never added on this network
)

// oldEntry is one row of the Old entries list.
type oldEntry struct {
	ID, Name, Kind, LastSeen string
	LongUnseen               bool
	// CloudDeviceID names the account-side entry a cloud removal targets.
	CloudDeviceID string
	// SameAsName: the live till this entry is provably the same device as
	// (tills.MarkSameDevice: same device id or the register link), "" if none.
	SameAsName string
	// Removable: the Remove button is offered. False only for an account-only
	// entry while the shop list's removal is switched off.
	Removable bool
}

// oldEntriesFrom lists every ghost of the merged view that is old enough to
// tidy (a till that just joined never is), account-only ones last.
func oldEntriesFrom(o tillsOthers) []oldEntry {
	var out []oldEntry
	add := func(r tills.Row, kind string) {
		out = append(out, oldEntry{ID: r.ID, Name: r.Name, Kind: kind, LastSeen: r.LastSeen, LongUnseen: r.LongUnseen,
			CloudDeviceID: r.CloudDeviceID, SameAsName: r.SameDeviceAs,
			Removable: kind != oldKindCloud || len(r.Actions) > 0})
	}
	for _, r := range o.OldEntries {
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

// oldBanner is what the Other tills banner says about the old entries it can
// remove right now.
type oldBanner struct {
	Removable int    // how many the button removes now
	Names     string // quoted names, "a" and "b"
	Phone     string // the live till they probably were, "" when unknown
}

func oldBannerFrom(entries []oldEntry, locale string, and string) oldBanner {
	var b oldBanner
	var names []string
	for _, e := range entries {
		if !e.Removable {
			continue
		}
		b.Removable++
		names = append(names, "“"+e.Name+"”")
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
	return o.Actionable
}

// registerTillsOldEntries wires POST /api/sync/tills/verify-pin: the inline
// panel checks the manager PIN with it before it posts the removals, so "Remove
// all" fails early on a wrong PIN before any entry is removed. It is not the
// gate: every removal route enforces the PIN itself (authorizeTillRemoval).
func registerTillsOldEntries(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/sync/tills/verify-pin", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authorizeTillRemoval(w, r, d); ok {
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

// RemoveURL is the route that removes this entry: the revoke for an entry this
// network holds, the cloud removal (device_id in the query, which the form
// read includes) for an account-only one.
func (e oldEntry) RemoveURL() string {
	if e.Kind == oldKindCloud {
		return "/api/sync/tills/cloud-remove?device_id=" + url.QueryEscape(e.CloudDeviceID)
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
