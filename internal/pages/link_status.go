package pages

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/updates"
)

// Link observability (ADR-0114 §10, ut-docs#2742).
//
// On a replica the status bar shows ONE connectivity indicator for the main
// till, derived from the link's presence (the main till's hello arrived and
// a frame came within 12 s, ADR-0114 §4) and the shared PrimaryWatch — not
// from the browser's navigator.onLine, which only says a network interface
// is up and read "Online" while the main till was gone (the report on the
// card). The browser's light is hidden next to it and comes back only as
// "No internet" when the network is really down (base.html).
//
// The derivation is role-agnostic on purpose: a satellite (ut-docs#2781)
// uses the same view as its connectivity light — for it the link IS the
// till's ability to take payment (ADR-0086) — so nothing here reads the
// replica role beyond "this till has a main till".

// linkState is what the connectivity chip says.
type linkState string

const (
	linkNone        linkState = ""            // not an additional till: no chip
	linkLinked      linkState = "linked"      // live link: changes arrive within a second
	linkPolling     linkState = "polling"     // no link (older main till, or still connecting): the 30 s pull
	linkUnreachable linkState = "unreachable" // the main till is gone: selling offline
)

// Update notes shown next to a linked chip, from the main till's hello
// version against this till's (the fleet-update states themselves are
// #2726; until then these are what the versions alone can say).
const (
	linkUpdateWaiting     = "update_waiting"      // the main till is newer: this till follows it
	linkMainUpdateWaiting = "main_update_waiting" // this till is newer: waiting for the main till
)

// linkInputs is everything the chip is derived from — gathered by
// linkInputsOf, kept separate so the rules are testable without a link.
type linkInputs struct {
	Replica          bool // this till has a main till (sync.primary_url)
	HasClient        bool
	Client           fleetlink.ClientStatus
	WatchUnreachable bool   // PrimaryWatch: repeated failed contacts
	WatchSince       string // RFC 3339, sync.last_contact_at
	LastContact      string // RFC 3339, sync.last_contact_at
	ThisVersion      string
	Now              time.Time // zero: time.Now()
}

// linkView is the chip's data.
type linkView struct {
	State          linkState
	Since          string // RFC 3339; set when unreachable and known
	Update         string // linkUpdateWaiting / linkMainUpdateWaiting / ""
	PluginsWaiting bool   // plugin updates wait for the main till (ADR-0011 §7)
}

// deriveLinkView applies the rules, in order:
//  1. no main till → no chip;
//  2. PrimaryWatch counts the main till unreachable → unreachable;
//  3. the link is live → linked;
//  4. an established link was lost and nothing has reached the main till
//     since the loss was noticed (no pull, no new link) → unreachable at
//     once, since the last frame — the chip turns within one 12 s presence
//     window instead of after the pull loop's three failed ticks;
//  5. the main till gives no answer to the link's attempts and the last
//     contact is older than PrimaryWatch's window (or there never was one)
//     → unreachable since that contact: the watch's own "already gone at
//     launch" rule, applied before its first tick — a replica restarted
//     while its main till is down would otherwise read "polling" for up
//     to 90 s (independent review);
//  6. otherwise → polling (an older main till without the link, or still
//     connecting): sync works, just not instantly.
func deriveLinkView(in linkInputs) linkView {
	if !in.Replica {
		return linkView{}
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	lostAt := in.Client.LostAt
	if in.WatchUnreachable {
		since := in.WatchSince
		if !lostAt.IsZero() && !contactAfter(since, lostAt) {
			since = lostAt.UTC().Format(time.RFC3339)
		}
		return linkView{State: linkUnreachable, Since: since}
	}
	if in.HasClient && in.Client.Linked {
		return linkView{State: linkLinked, Update: linkUpdateNote(in.ThisVersion, in.Client.MainVersion)}
	}
	if in.HasClient && !lostAt.IsZero() && !contactAfter(in.LastContact, lostSeen(in.Client)) {
		return linkView{State: linkUnreachable, Since: lostAt.UTC().Format(time.RFC3339)}
	}
	if in.HasClient && !in.Client.FailedAt.IsZero() && !contactAfter(in.LastContact, now.Add(-discovery.UnreachableWindow)) {
		return linkView{State: linkUnreachable, Since: strings.TrimSpace(in.LastContact)}
	}
	return linkView{State: linkPolling}
}

// lostSeen is when the loss was noticed (LostAt when not recorded).
func lostSeen(c fleetlink.ClientStatus) time.Time {
	if c.LostSeen.IsZero() {
		return c.LostAt
	}
	return c.LostSeen
}

// contactAfter reports whether the RFC 3339 contact time is after t (at
// the second resolution sync.last_contact_at is stored in).
func contactAfter(contact string, t time.Time) bool {
	c, err := time.Parse(time.RFC3339, strings.TrimSpace(contact))
	return err == nil && c.After(t.Truncate(time.Second))
}

// linkUpdateNote compares this till's version with the main till's. Only
// release versions compare; a dev build or an unknown version says nothing.
func linkUpdateNote(this, main string) string {
	if !releaseVersion(this) || !releaseVersion(main) {
		return ""
	}
	// updates.Newer compares bare dotted numbers; a leading v is common
	// in tags (v1.4.0) and would read as 0.
	this = strings.TrimPrefix(strings.TrimSpace(this), "v")
	main = strings.TrimPrefix(strings.TrimSpace(main), "v")
	switch {
	case updates.Newer(main, this):
		return linkUpdateWaiting
	case updates.Newer(this, main):
		return linkMainUpdateWaiting
	}
	return ""
}

// releaseVersion: "1.2.3" or "v1.2.3" — a digit after the optional v.
func releaseVersion(v string) bool {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	return v != "" && v[0] >= '0' && v[0] <= '9'
}

// linkInputsOf reads the inputs for this till now.
func linkInputsOf(ctx context.Context, d *common.Deps) linkInputs {
	in := linkInputs{Replica: d.SyncPrimaryURL(ctx) != "", ThisVersion: buildinfo.Version, Now: time.Now()}
	if !in.Replica {
		return in
	}
	if d.LinkClient != nil {
		in.HasClient, in.Client = true, d.LinkClient.Status()
	}
	if d.PrimaryWatch != nil {
		in.WatchSince, in.WatchUnreachable = d.PrimaryWatch.Unreachable(ctx)
	}
	in.LastContact, _, _ = d.Settings.Get(ctx, "sync.last_contact_at")
	return in
}

// replicaLinkView is the chip's view for this till now.
func replicaLinkView(ctx context.Context, d *common.Deps) linkView {
	v := deriveLinkView(linkInputsOf(ctx, d))
	if v.State == linkUnreachable {
		v.PluginsWaiting = plugins.CurrentPendingUpdates().Count > 0
	}
	return v
}

// renderLinkChip renders the chip for v; nothing at all when there is no
// main till.
func renderLinkChip(v linkView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if v.State == linkNone {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderPartial("ui/partials/main_till_status.html", map[string]any{
			"state":          string(v.State),
			"since":          v.Since,
			"update":         v.Update,
			"pluginsWaiting": v.PluginsWaiting,
		})(w, r)
	}
}
