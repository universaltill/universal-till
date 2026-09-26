package pages

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Main-till link, replica side (ADR-0114 §2–§4/§11, ut-docs#2735).
//
// An additional till dials its main till's GET /api/sync/link — only when
// the main till advertises `link: 1` on GET /api/sync/ping; an older main
// till is polled exactly as before. The link is a nudge (§3): a `sync`
// frame, or a hello whose cursors differ from ours, kicks the ordinary HTTP
// admin pull (d.SyncPullNow), which stays the source of truth. While linked
// that pull runs every 5 min instead of every 30 s.
//
// The link shares the pull loop's PrimaryWatch (§4): the main till's hello
// is a successful contact, an established link lost without a bye is a
// failed one, and when re-discovery switches sync.primary_url the link
// redials the new address (primaryContactFailed → LinkClient.Redial).
//
// Offline-first: nothing in a sale waits on any of this.

// linkProbeTimeout bounds the ping that decides whether to dial.
const linkProbeTimeout = 5 * time.Second

// newSyncLinkClient builds this till's link client over opts' timings
// (fleetlink.DefaultClientOptions in production). Every callback reads the
// settings at call time, so a re-pairing or a re-discovered address is
// picked up on the next attempt.
func newSyncLinkClient(d *common.Deps, opts fleetlink.ClientOptions) *fleetlink.Client {
	posRepo := data.NewPOSRepo(d.Db)
	probeClient := &http.Client{
		Timeout: linkProbeTimeout,
		// The bearer goes to the main till's address only.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	get := func(ctx context.Context, k string) string {
		v, _, _ := d.Settings.Get(ctx, k)
		return strings.TrimSpace(v)
	}

	opts.Target = func(ctx context.Context) (fleetlink.Target, bool) {
		t := fleetlink.Target{BaseURL: get(ctx, "sync.primary_url"), Bearer: get(ctx, "sync.bearer")}
		return t, t.BaseURL != "" && t.Bearer != ""
	}
	opts.Probe = func(ctx context.Context, t fleetlink.Target) (int, error) {
		level, version, err := probeLinkLevel(ctx, probeClient, t)
		// The main till's version is what this till follows (ut-docs#2738);
		// kept per-till (sync.*) so a polling replica has a target too.
		if err == nil && version != "" {
			if cur, _, _ := d.Settings.Get(ctx, keyMainVersion); cur != version {
				_ = d.Settings.Set(ctx, keyMainVersion, version)
			}
		}
		return level, err
	}
	opts.Hello = func(ctx context.Context) fleetlink.Hello {
		return fleetlink.Hello{
			TillID:       get(ctx, "sync.till_id"),
			Role:         "replica",
			Version:      buildinfo.Version,
			Platform:     runtime.GOOS + "/" + runtime.GOARCH,
			SyncProtocol: fleetlink.SyncProtocolLevel,
			Cursors: fleetlink.Cursors{
				Admin:   get(ctx, "sync.pull_version"),
				Plugins: get(ctx, "sync.plugins_version"),
				Stock:   get(ctx, "sync.stock_version"),
			},
			// This replica's OWN cloud device id (ut-docs#2730), separate
			// from TillID (the LAN pairing id) — lets the main till's
			// status frame to the cloud (ut-docs#2897) name this peer as
			// my.'s Tills rows and Live panel actually key it, instead of
			// the raw pairing id. Empty until this till's own enrolment
			// has minted one; the main till then just carries an empty
			// device id, same as an older replica.
			CloudDeviceID: enroll.CurrentStatus().DeviceID,
		}
	}
	opts.Report = func(ctx context.Context) fleetlink.Report {
		depth, _ := posRepo.CountLocalSalesSince(ctx, get(ctx, "sync.push_cursor"))
		return fleetlink.Report{
			Version:        buildinfo.Version,
			UpdateState:    "idle", // #2726 fills the fleet-update states
			PushQueueDepth: depth,
			TLSPinned:      false, // #2736
		}
	}
	opts.OnHello = func(ctx context.Context, main fleetlink.Hello) {
		markLinkContact(ctx, d)
		primaryContactOK(ctx, d)
		// A cursor we don't have means something changed while we were
		// not linked: pull now instead of at the (now 5-min) floor.
		if main.Cursors.Admin != get(ctx, "sync.pull_version") ||
			main.Cursors.Plugins != get(ctx, "sync.plugins_version") ||
			main.Cursors.Stock != get(ctx, "sync.stock_version") {
			d.RequestSyncPull()
		}
		logging.L().Infof("sync link: linked to the main till %s (%s)", main.TillID, main.Version)
	}
	opts.OnSync = func(_ context.Context, scopes []fleetlink.Scope) {
		// One full pull covers every scope the HTTP pull carries (admin,
		// plugins and stock ride the same tick); held sales, tables and
		// orders are read live from the main till, so a nudge naming only
		// those costs no pull. Kicks coalesce, so a burst is one pull.
		if linkScopesNeedPull(scopes) {
			d.RequestSyncPull()
		}
		// ut-docs#2858: an order held, resumed or moved on another till
		// changes what this till's Open orders badge counts; move the held
		// generation so an open sale screen here re-reads it now instead of
		// at its next reload.
		if linkScopesTouchHeld(scopes) {
			d.MarkHeldChanged()
		}
	}
	opts.OnCloudCheckin = func(context.Context) {
		// The main till relayed a cloud nudge (ut-docs#2893): check in with
		// the cloud now instead of at the next 2-min tick. Single-flight —
		// the same capacity-1 kick the main till's own cloud link uses.
		select {
		case d.CloudSyncNow <- struct{}{}:
		default: // a check-in is already pending (or no loop: nil channel)
		}
	}
	opts.OnLost = func(ctx context.Context, cause string) {
		primaryContactFailed(ctx, d, cause)
	}
	opts.OnRevoked = func(context.Context) {
		logging.L().Warnf("sync link: the main till no longer accepts this till's pairing — link stopped; this till keeps selling offline until it is paired again")
	}
	opts.WhileLinked = func(ctx context.Context) {
		refreshLinkContact(ctx, d)
	}
	return fleetlink.NewClient(opts)
}

// linkScopesNeedPull reports whether a sync nudge names a scope the admin
// pull tick actually fetches.
func linkScopesNeedPull(scopes []fleetlink.Scope) bool {
	for _, s := range scopes {
		switch s {
		case fleetlink.ScopeAdmin, fleetlink.ScopePlugins, fleetlink.ScopeStock:
			return true
		}
	}
	return false
}

// linkScopesTouchHeld reports whether a sync nudge names held sales.
func linkScopesTouchHeld(scopes []fleetlink.Scope) bool {
	for _, s := range scopes {
		if s == fleetlink.ScopeHeldSales {
			return true
		}
	}
	return false
}

// markLinkContact records contact with the main till now: its hello is one.
func markLinkContact(ctx context.Context, d *common.Deps) {
	_ = d.Settings.Set(ctx, "sync.last_contact_at", time.Now().UTC().Format(time.RFC3339))
}

// linkContactWindow is how long after its last successful pull a linked
// till keeps counting the link as contact: one linked floor plus one
// unlinked interval of grace.
const linkContactWindow = syncPullEveryLinked + syncPullEvery

// refreshLinkContact keeps sync.last_contact_at fresh while linked — with
// the pull at a 5-min floor the sync chip's 90 s freshness window would
// otherwise read "offline" between pulls — but only while the pull itself
// last succeeded within linkContactWindow. ut-docs#807's rule holds: that
// key must never say "healthy" over a pull that keeps failing, and a live
// link over a stuck pull is exactly that case.
func refreshLinkContact(ctx context.Context, d *common.Deps) {
	v, _, _ := d.Settings.Get(ctx, "sync.last_pull_ok_at")
	if !withinLast(strings.TrimSpace(v), linkContactWindow) {
		return
	}
	markLinkContact(ctx, d)
}

// probeLinkLevel asks the main till which link level it serves: GET
// /api/sync/ping's "link" (0 when absent — an older main till), and its
// version ("" when absent or not a release number, ut-docs#2738).
func probeLinkLevel(ctx context.Context, client *http.Client, t fleetlink.Target) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(t.BaseURL, "/")+"/api/sync/ping", nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+t.Bearer)
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return 0, "", fleetlink.ErrUnauthorized
	case resp.StatusCode != http.StatusOK:
		return 0, "", errors.New("sync link: ping answered " + resp.Status)
	}
	var out struct {
		Data struct {
			Link    int    `json:"link"`
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out); err != nil {
		return 0, "", err
	}
	// Device input: only a release number is kept.
	version := strings.TrimSpace(out.Data.Version)
	if !releaseVersion(version) {
		version = ""
	}
	return out.Data.Link, version, nil
}

// StartSyncLinkClient runs d.LinkClient until ctx ends, joined by app.Run's
// drain (wg). On a main or standalone till it only re-reads its settings
// every 30 s; on shutdown it says bye and closes the link.
func StartSyncLinkClient(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	if d.LinkClient == nil {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.LinkClient.Run(ctx)
	}()
}
