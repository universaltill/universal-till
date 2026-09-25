package pages

import (
	"context"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Main-till link, main side (ADR-0114 §1/§2/§4/§8/§11, ut-docs#2734).
//
// Additional tills (and later satellites) dial GET /api/sync/link; nothing
// dials out from here. The link carries presence, hello state and coalesced
// `sync` nudges; the data itself still moves over the existing /api/sync/*
// pulls, which stay the source of truth. Plain HTTP for now — pinned TLS is
// #2736; the replica client is #2735.

// linkAdminWatchInterval is how often the main till checks the admin
// generation counter while at least one till is linked. One single-row
// SELECT per second, only while linked.
const linkAdminWatchInterval = time.Second

// newSyncLinkHub builds the hub. adminRepo is the same instance
// registerSyncAdmin serves GET /api/sync/admin from, so the hello's admin
// cursor comes from the one generation-keyed bundle cache instead of a
// second copy of it (memory-bounded caches rule).
func newSyncLinkHub(d *common.Deps, cfg fleetlink.Config, adminRepo *data.SyncAdminRepo) *fleetlink.Hub {
	settingsRepo := data.NewSettingsRepo(d.Db)
	pluginsRepo := data.NewSyncPluginsRepo(d.Db)
	stockRepo := data.NewSyncStockRepo(data.NewPOSRepo(d.Db))
	tills := data.NewTillsRepo(d.Db)
	return fleetlink.NewHub(fleetlink.HubOptions{
		Config: cfg,
		Hello: func(ctx context.Context, _ string) fleetlink.Hello {
			h := fleetlink.Hello{
				Role:         "main",
				Version:      buildinfo.Version,
				Platform:     runtime.GOOS + "/" + runtime.GOARCH,
				SyncProtocol: fleetlink.SyncProtocolLevel,
			}
			if id, err := discovery.TillID(ctx, settingsRepo); err == nil {
				h.TillID = id
			}
			// Cursors are best-effort: an empty one only makes the replica
			// pull that scope, which is always safe.
			if fp, err := adminRepo.AdminFingerprint(ctx); err == nil {
				h.Cursors.Admin = fp
			}
			if b, err := pluginsRepo.DumpActivePlugins(ctx); err == nil {
				h.Cursors.Plugins = b.Fingerprint()
			}
			if b, err := stockRepo.DumpStock(ctx); err == nil {
				h.Cursors.Stock = b.Fingerprint()
			}
			return h
		},
		// A linked till may poll far less (#2735's 5-min floor), but
		// table-claim TTLs read tills.last_seen_at: keep it fresh from the
		// link, at most every 30 s per till.
		OnFrame: func(tillID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tills.TouchLastSeen(ctx, tillID); err != nil {
				logging.L().Warnf("sync link: touch last seen for %s: %v", tillID, err)
			}
		},
	})
}

// registerSyncLink mounts GET /api/sync/link. Auth-middleware exempt (the
// exempt list carries it); syncTill authenticates the upgrade request.
func registerSyncLink(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	mux.HandleFunc("GET /api/sync/link", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, tills)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Links terminate on the main till only; a replica's tills table
		// is a synced copy with redacted bearers, so this is defence in
		// depth next to syncTill.
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Error(w, "connect to the main till", http.StatusConflict)
			return
		}
		if d.Link == nil {
			http.Error(w, "link unavailable", http.StatusServiceUnavailable)
			return
		}
		d.Link.Serve(w, r, till.ID)
	})
}

// StartSyncLink runs the link's background side, joined to app.Run's drain:
// the admin-generation watch, and on shutdown a `bye` to every linked till
// before their connections close (http.Server.Shutdown does not track
// hijacked connections, so without this they would outlive the server).
func StartSyncLink(ctx context.Context, d *common.Deps, wg *sync.WaitGroup, adminRepo *data.SyncAdminRepo) {
	if d.Link == nil {
		return
	}
	runLinkAdminWatch(ctx, d, wg, linkAdminWatchInterval, adminRepo)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		d.Link.Close()
	}()
}

// runLinkAdminWatch nudges `admin` whenever the admin bundle changes: it
// watches sync_admin_version, bumped by a trigger on every admin-bundle
// table, so this one watch covers every writer: the back office, a
// write-through from another till, a my. directive, the admin-bundle apply —
// users, roles and PINs included (#2731) — without a hook at each call site.
// A moved generation is then confirmed against the bundle fingerprint, so a
// per-till settings write never nudges (ut-docs#2792).
func runLinkAdminWatch(ctx context.Context, d *common.Deps, wg *sync.WaitGroup, every time.Duration, adminRepo *data.SyncAdminRepo) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		var last int64
		var lastFP string
		var lastErrLog time.Time
		baseline := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if d.Link.Len() == 0 {
				baseline = false // a new link's hello carries the cursor
				continue
			}
			gen, tracked := adminRepo.AdminGeneration(ctx)
			if !tracked {
				continue
			}
			if baseline && gen == last {
				continue
			}
			// The settings trigger also bumps the generation for per-till
			// keys that never travel (ut-docs#2792): nudge only when the
			// bundle a replica would pull actually changed. On a moved
			// generation this is the same one scan the replica's pull
			// would cost anyway, and it primes the shared cache for it.
			fp, err := adminRepo.AdminFingerprint(ctx)
			if err != nil {
				// Retry next tick; a replica's own poll still converges.
				if time.Since(lastErrLog) > time.Minute {
					logging.L().Warnf("sync link: admin fingerprint for the nudge watch: %v", err)
					lastErrLog = time.Now()
				}
				continue
			}
			if baseline && fp != lastFP {
				d.NudgeLink(fleetlink.ScopeAdmin)
			}
			last, lastFP, baseline = gen, fp, true
		}
	}()
}
