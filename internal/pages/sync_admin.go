package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// LAN sync D2b + D4 (ADR-0011): the primary serves its admin state
// (catalog, users, shop settings, translations) as one fingerprinted
// bundle; replicas poll it and apply wholesale — primary wins, deletes
// propagate. The sync chip surfaces state without ever blocking checkout
// (ADR-0003).

// adminBundleResponse is the wire shape of GET /api/sync/admin.
type adminBundleResponse struct {
	Version   string           `json:"version"`
	Unchanged bool             `json:"unchanged"`
	Bundle    data.AdminBundle `json:"bundle"`
}

// stockBundleResponse is the wire shape of GET /api/sync/stock (D3b).
type stockBundleResponse struct {
	Version   string           `json:"version"`
	Unchanged bool             `json:"unchanged"`
	Bundle    data.StockBundle `json:"bundle"`
}

// pluginsBundleResponse is the wire shape of GET /api/sync/plugins
// (ut-docs#460): the primary's active marketplace-plugin registry.
type pluginsBundleResponse struct {
	Version   string             `json:"version"`
	Unchanged bool               `json:"unchanged"`
	Bundle    data.PluginsBundle `json:"bundle"`
}

func registerSyncAdmin(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	adminRepo := data.NewSyncAdminRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	// Primary side: admin-state bundle. `?have=` lets a replica poll
	// cheaply — matching fingerprint returns no body worth applying.
	mux.HandleFunc("GET /api/sync/admin", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := syncTill(r, tills); !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
			return
		}
		bundle, err := adminRepo.DumpAdmin(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_admin", err)
			return
		}
		resp := adminBundleResponse{Version: bundle.Fingerprint()}
		if r.URL.Query().Get("have") == resp.Version {
			resp.Unchanged = true
		} else {
			resp.Bundle = bundle
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": resp, "error": nil})
	})

	// Primary side: shop-wide stock levels (D3b). Every till's sale journal
	// lands here, so this aggregate is the authoritative on-hand; replicas
	// poll it and correct their local levels to match.
	stockRepo := data.NewSyncStockRepo(posRepo)
	mux.HandleFunc("GET /api/sync/stock", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := syncTill(r, tills); !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
			return
		}
		bundle, err := stockRepo.DumpStock(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_admin", err)
			return
		}
		resp := stockBundleResponse{Version: bundle.Fingerprint()}
		if r.URL.Query().Get("have") == resp.Version {
			resp.Unchanged = true
		} else {
			resp.Bundle = bundle
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": resp, "error": nil})
	})

	// Primary side: the active marketplace-plugin registry (ut-docs#460,
	// ADR-0011 amendment 2026-08-08). Same auth and same `?have=`
	// fingerprint poll as the admin/stock bundles. Registry only — never
	// plugin bytes: a replica converges by re-fetching each listing from
	// the marketplace itself through the Ed25519-verified installer.
	pluginsRepo := data.NewSyncPluginsRepo(d.Db)
	mux.HandleFunc("GET /api/sync/plugins", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := syncTill(r, tills); !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
			return
		}
		bundle, err := pluginsRepo.DumpActivePlugins(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_admin", err)
			return
		}
		resp := pluginsBundleResponse{Version: bundle.Fingerprint()}
		if r.URL.Query().Get("have") == resp.Version {
			resp.Unchanged = true
		} else {
			resp.Bundle = bundle
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": resp, "error": nil})
	})

	// D4 status chip (nav, both sides). Renders empty unless this till is
	// a replica or a primary with enrolled tills.
	mux.HandleFunc("GET /ui/sync-chip", func(w http.ResponseWriter, r *http.Request) {
		get := func(k string) string {
			v, _, _ := d.Settings.Get(r.Context(), k)
			return strings.TrimSpace(v)
		}
		if primary := d.SyncPrimaryURL(r.Context()); primary != "" {
			queued, _ := posRepo.CountLocalSalesSince(r.Context(), get("sync.push_cursor"))
			fresh := withinLast(get("sync.last_contact_at"), 90*time.Second)
			class := "ok"
			if !fresh {
				class = "warn"
			}
			label := get("sync.till_name")
			if label == "" {
				label = strings.TrimSuffix(get("sync.receipt_prefix"), "-")
			}
			httpx.RenderPartial("ui/partials/sync_chip.html", map[string]any{
				"isReplica": true,
				"class":     class,
				"label":     label,
				"queued":    queued,
				"offline":   !fresh,
			})(w, r)
			return
		}
		list, err := tills.ListTills(r.Context())
		if err != nil || len(list) == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		class := "ok"
		for _, t := range list {
			if !withinLast(t.LastSeenAt, 2*time.Minute) {
				class = "warn"
				break
			}
		}
		// ut-docs#1133 (ADR-0065 follow-up): a quarantined LAN-sync journal
		// entry never becomes a sale and both ends of that pull otherwise
		// read as fully caught-up (see registerSyncQuarantinePage's doc
		// comment) -- surfacing a nonzero count on the nav chip, which
		// renders on every authenticated page, is a much stronger standing
		// signal than the point-in-time Problems-panel Warnf that used to
		// be the only trace. Best-effort like the rest of this handler: a
		// read error just leaves the chip's healthy state unchanged rather
		// than failing the whole chip render.
		quarantined, qErr := posRepo.CountJournalQuarantine(r.Context())
		if qErr != nil {
			logging.L().Errorf("count sync journal quarantine: %v", qErr)
			quarantined = 0
		}
		if quarantined > 0 {
			class = "warn"
		}
		// ut-docs#405: this used to show only a bare replica COUNT — there
		// was nowhere else to read a name from before #396 added
		// tillNameOrDefault. Now it shows the same "this till's own name"
		// every other identity surface (Settings, /tills) already shows.
		label := tillNameOrDefault(r.Context(), d, httpx.ResolveLocale(w, r))
		httpx.RenderPartial("ui/partials/sync_chip.html", map[string]any{
			"isReplica":   false,
			"class":       class,
			"label":       label,
			"count":       len(list),
			"quarantined": quarantined,
		})(w, r)
	})
}

// withinLast reports whether an RFC3339 timestamp is fresher than d.
func withinLast(ts string, d time.Duration) bool {
	t, err := time.Parse(time.RFC3339, ts)
	return err == nil && time.Since(t) < d
}

// runSyncLoop drives a sync tick every 30s until ctx ends — the shared
// harness of the journal push and the admin pull. wg registers the goroutine
// with app.Run's shutdown drain (ut-docs#153), same join shape as
// cloudsync.Start: wg.Add before the goroutine starts, wg.Done on every exit
// path, so a caller waiting on wg can prove this loop actually stopped
// before database.Close() runs. kick, when non-nil, requests one extra tick
// immediately (ut-docs#404: a replica pushes its journal right after a local
// sale instead of waiting out the 30s tick); nil means schedule-only — a nil
// channel never receives, so that select arm simply never fires.
func runSyncLoop(ctx context.Context, wg *sync.WaitGroup, kick <-chan struct{}, tick func()) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tick()
			case <-kick:
				tick()
			}
		}
	}()
}

// StartSyncPull runs the replica-side drift loop: every 30s fetch the
// primary's admin bundle and apply it when the fingerprint moved. refresh
// re-derives in-memory state (theme, tax engine, i18n) after an apply. wg
// registers the loop with app.Run's shutdown drain (ut-docs#153) — the
// caller must pass bgCtx (not ctx), same requirement as StartCloudSync.
func StartSyncPull(ctx context.Context, d *common.Deps, refresh func(context.Context), wg *sync.WaitGroup) {
	client := &http.Client{Timeout: 60 * time.Second}
	runSyncLoop(ctx, wg, nil, func() { syncPullTick(ctx, d, client, refresh) })
}

// syncPullTick is one tick of the replica-side drift loop, extracted from
// StartSyncPull so it can be driven directly in tests instead of only via
// the real 30s ticker.
func syncPullTick(ctx context.Context, d *common.Deps, client *http.Client, refresh func(context.Context)) {
	adminRepo := data.NewSyncAdminRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	get := func(k string) string {
		v, _, _ := d.Settings.Get(ctx, k)
		return strings.TrimSpace(v)
	}
	primary, bearer := get("sync.primary_url"), get("sync.bearer")
	if primary == "" || bearer == "" {
		return
	}
	url := strings.TrimSuffix(primary, "/") + "/api/sync/admin?have=" + get("sync.pull_version")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Infof("sync pull: primary unreachable (%v) — will retry", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Errorf("sync pull rejected: %s", resp.Status)
		return
	}
	var out struct {
		Data adminBundleResponse `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		logging.L().Errorf("sync pull: bad response: %v", err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// Files ride alongside the row data: item images can change
	// without moving the admin fingerprint, so this runs every tick
	// (the manifest is cheap; only missing/changed files download).
	syncItemAssets(ctx, client, primary, bearer)
	if !out.Data.Unchanged {
		if err := adminRepo.ApplyAdmin(ctx, out.Data.Bundle); err != nil {
			logging.L().Errorf("sync pull: apply failed: %v", err)
			// ut-docs#807: sync.last_contact_at deliberately NOT set here.
			// It backs the sync chip's freshness signal (withinLast check
			// below) — setting it unconditionally right after the HTTP
			// round-trip (the pre-#807 behavior) made the chip show healthy
			// contact even on a tick whose apply failed and whose
			// sync.pull_version never advanced, hiding a stuck admin pull.
			// Note this is a strict improvement, not a complete fix: an
			// otherwise-healthy replica whose sales journal is still
			// pushing normally also refreshes this same key from the push
			// side (sync_sales.go), which can mask a stuck admin pull too —
			// pre-existing, not introduced or fixed here.
			return
		}
		_ = d.Settings.Set(ctx, "sync.pull_version", out.Data.Version)
		_ = d.Settings.Set(ctx, "sync.last_pull_at", now)
		_ = posRepo.InsertAudit(ctx, nil, "system", "till", get("sync.till_id"), "admin_pulled",
			map[string]any{"version": out.Data.Version}, now, "")
		refresh(ctx)
		logging.L().Infof("sync pull: admin state %s applied from the primary", out.Data.Version)
	}
	// Reached only when the poll round-tripped AND (nothing changed, or the
	// apply above actually succeeded) — see the early return in the failure
	// branch just above.
	_ = d.Settings.Set(ctx, "sync.last_contact_at", now)

	// ut-docs#460 — the plugin set follows the primary. Best-effort like
	// everything else in this tick: syncPullPlugins logs and returns on any
	// failure, never panics and never causes THIS function to return early,
	// so the stock section below still runs. Ordered after the admin apply
	// (a freshly propagated plugin's global plugin_settings rows land on a
	// subsequent admin pull, once installed locally) and before the stock
	// section (whose push-queue gate may legitimately end the tick).
	syncPullPlugins(ctx, d, client, primary, bearer)

	// D3b — stock levels follow the primary (it has every till's sale
	// journal, so its aggregate is the shop truth). Runs AFTER the admin
	// apply (corrections may reference freshly synced items/variants) and
	// only when this till's own journal is fully pushed — otherwise the
	// primary hasn't counted our latest sales yet and a correction would
	// briefly re-add sold stock.
	if queued, err := posRepo.CountLocalSalesSince(ctx, get("sync.push_cursor")); err != nil || queued > 0 {
		return
	}
	stockRepo := data.NewSyncStockRepo(posRepo)
	surl := strings.TrimSuffix(primary, "/") + "/api/sync/stock?have=" + get("sync.stock_version")
	sreq, err := http.NewRequestWithContext(ctx, http.MethodGet, surl, nil)
	if err != nil {
		return
	}
	sreq.Header.Set("Authorization", "Bearer "+bearer)
	sresp, err := client.Do(sreq)
	if err != nil {
		return // primary just answered the admin poll; transient — next tick retries
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		logging.L().Errorf("stock sync pull rejected: %s", sresp.Status)
		return
	}
	var sout struct {
		Data stockBundleResponse `json:"data"`
	}
	if err := json.NewDecoder(sresp.Body).Decode(&sout); err != nil {
		logging.L().Errorf("stock sync pull: bad response: %v", err)
		return
	}
	if sout.Data.Unchanged {
		return
	}
	locID, err := posRepo.EnsureStockLocation(ctx)
	if err != nil {
		logging.L().Errorf("stock sync pull: no stock location: %v", err)
		return
	}
	corrections, err := stockRepo.ApplyStockLevels(ctx, sout.Data.Bundle, locID)
	if err != nil {
		logging.L().Errorf("stock sync pull: apply failed after %d corrections: %v", corrections, err)
		return
	}
	_ = d.Settings.Set(ctx, "sync.stock_version", sout.Data.Version)
	if corrections > 0 {
		logging.L().Infof("stock sync: %d level corrections applied from the primary (%s)", corrections, sout.Data.Version)
	}
}

// syncPullPlugins is the plugin-set section of syncPullTick (ut-docs#460,
// ADR-0011 amendment 2026-08-08): fetch the primary's active
// marketplace-plugin registry and converge this replica's installed set to
// it. Installs go through cloudInstallPlugin — the same
// download-token + Ed25519-verification MarketplaceInstaller path a manual
// install button click takes, just triggered by the tick instead of a
// manager; the primary never ships plugin bytes. Uninstalls go through
// cloudRemovePlugin, the shared uninstall core the manual handler and the
// cloud directive already use.
//
// Best-effort throughout: every failure is logged and skipped, and the
// pull fingerprint (sync.plugins_version) is only advanced once a tick
// converges with NO failures, so a transient marketplace outage is retried
// on every subsequent tick until the set matches. A recover() at the top
// keeps the "never panics" promise honest — runSyncLoop has no recover of
// its own, so a panic in the reload/install path would otherwise kill the
// whole sync loop for good. Never called on the checkout path.
//
// Steady state is NOT a no-op: an Unchanged poll only means the PRIMARY's
// set hasn't moved — it says nothing about local drift (a failed reload,
// anything that slips past the replica guards). The local half of this
// diff reads the plugins table (InstalledPluginVersion): both the version
// AND the install lifecycle state — a row that claims the right version
// while its FILES are missing/unloadable is flipped to
// install_state='broken' by WasmRuntime.Sync and treated as drift here
// (ut-docs#368: a join snapshot writes rows, never bytes — this is how
// such a plugin heals within one tick instead of hiding behind a
// version-only diff forever). The last-applied bundle
// rows are persisted locally
// (sync.plugins_bundle, alongside sync.plugins_version), and on an
// Unchanged response the same diff/converge loop re-runs against that
// cached row set every tick. The `?have=` fingerprint still saves the
// primary→replica payload; only the local diff (a few dozen rows, cheap
// version lookups) repeats.
//
// Scope boundary (intentional, ut-docs#460): only plugins with a
// listing_id — i.e. installed from the marketplace — propagate. A
// file-imported plugin (import-from-file / offline provisioning) has no
// listing on either side: the primary's dump can't include it (no
// plugin_install_status row exists), and the prune below only ever removes
// plugins named by a local install-status record, so a replica-local file
// import is never touched (and import-from-file stays usable on every
// till, replicas included). Provision those per till, as before.
func syncPullPlugins(ctx context.Context, d *common.Deps, client *http.Client, primary, bearer string) {
	defer func() {
		if r := recover(); r != nil {
			logging.L().Errorf("plugin sync: recovered from panic (will retry next tick): %v", r)
		}
	}()
	get := func(k string) string {
		v, _, _ := d.Settings.Get(ctx, k)
		return strings.TrimSpace(v)
	}
	url := strings.TrimSuffix(primary, "/") + "/api/sync/plugins?have=" + get("sync.plugins_version")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		return // primary just answered the admin poll; transient — next tick retries
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// A not-yet-upgraded primary answers 404 here — quietly wait for it
		// to upgrade rather than spamming the log every 30s.
		if resp.StatusCode != http.StatusNotFound {
			logging.L().Errorf("plugin sync pull rejected: %s", resp.Status)
		}
		return
	}
	var out struct {
		Data pluginsBundleResponse `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		logging.L().Errorf("plugin sync pull: bad response: %v", err)
		return
	}

	if out.Data.Unchanged {
		// Primary unchanged — still reconcile locally against the cached
		// last-applied rows so local drift heals instead of hiding behind
		// the fingerprint forever.
		var cached data.PluginsBundle
		raw, _, _ := d.Settings.Get(ctx, "sync.plugins_bundle")
		if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &cached) != nil {
			// No usable cache (pre-cache release, or corrupt). Clear the
			// fingerprint so the next tick full-fetches and rebuilds it.
			_ = d.Settings.Set(ctx, "sync.plugins_version", "")
			return
		}
		convergePluginSet(ctx, d, cached.Plugins)
		return
	}

	if convergePluginSet(ctx, d, out.Data.Bundle.Plugins) {
		// Advance the fingerprint (and the reconcile cache) only on full
		// convergence, so any skipped install/uninstall is retried next
		// tick instead of silently forgotten behind an unchanged poll.
		if raw, err := json.Marshal(out.Data.Bundle); err == nil {
			_ = d.Settings.Set(ctx, "sync.plugins_bundle", string(raw))
		}
		_ = d.Settings.Set(ctx, "sync.plugins_version", out.Data.Version)
	}
}

// brokenRefetchMaxAttempts and brokenRefetchBackoffTicks bound the broken-
// plugin marketplace re-fetch (ut-docs#368 round-2 review): a plugin can be
// broken for a reason a re-fetch can never fix (e.g. a binary that installs
// fine but can never load on this device), and without a cap the loop would
// hit the marketplace on every 30s tick forever. After
// brokenRefetchMaxAttempts consecutive attempts without an observed heal,
// the loop attempts only every brokenRefetchBackoffTicks-th tick (~5 min at
// the 30s cadence) — degraded, not stopped, so a marketplace outage that
// merely OUTLASTS the burst still recovers on its own.
const (
	brokenRefetchMaxAttempts  = 5
	brokenRefetchBackoffTicks = 10
)

// convergePluginSet makes this till's marketplace-installed plugin set match
// the given primary registry rows — the shared diff loop of both the
// changed-bundle and steady-state (cached rows) paths of syncPullPlugins.
// "Match" covers install state, not just version: a locally-broken plugin at
// the otherwise-correct version (row present, binary missing/unloadable —
// ut-docs#368) is reconverged through the same verified re-fetch as a
// version mismatch. Returns true only if every needed install/uninstall
// succeeded.
func convergePluginSet(ctx context.Context, d *common.Deps, rows []data.PluginSyncRow) bool {
	repo := data.NewSyncPluginsRepo(d.Db)
	statusStore := plugins.NewInstallStatusStore(d.Db)
	converged := true

	// Installs/updates: listing active on the primary, missing, at a
	// different version locally, or locally BROKEN (right version, files
	// missing/unloadable — ut-docs#368) → re-fetch from the marketplace
	// (verified), pinned to the PRIMARY's version — not the marketplace's
	// latest — so a primary deliberately held behind latest doesn't fork
	// its replicas. The broken case rides the exact same Ed25519-verified
	// cloudInstallPluginVersion path as a version mismatch: zero new
	// transfer mechanism, the install simply re-lands the files and flips
	// the row back to 'installed'.
	inPrimary := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.ListingID == "" || row.PluginID == "" {
			continue // defense in depth; DumpActivePlugins already filters these
		}
		inPrimary[row.ListingID] = true
		version, installState, installed, err := repo.InstalledPluginVersion(ctx, row.PluginID)
		if err != nil {
			logging.L().Errorf("plugin sync: read local state for %s: %v", row.PluginID, err)
			converged = false
			continue
		}
		if installed && version == row.Version && installState != data.PluginStateBroken {
			clearBrokenRefetch(d, row.ListingID)
			continue
		}
		if installed && installState == data.PluginStateBroken {
			// Bounded retry (round-2 review): a re-fetch can't fix every
			// breakage (a binary that installs fine but can never load on
			// this device re-breaks on the very next reload), so past
			// brokenRefetchMaxAttempts consecutive attempts without an
			// observed heal the loop degrades to one attempt every
			// brokenRefetchBackoffTicks ticks instead of every tick.
			if !shouldRefetchBroken(d, row.ListingID, row.Version) {
				converged = false // still broken — just not retrying this tick
				continue
			}
			logging.L().Warnf("plugin sync: %s (%s@%s) is broken locally (registered but not loadable) — re-fetching from the marketplace",
				row.PluginName, row.ListingID, row.Version)
		}
		if _, err := cloudInstallPluginVersion(ctx, d, row.ListingID, row.Version); err != nil {
			logging.L().Warnf("plugin sync: install %s (%s@%s) from the marketplace failed (will retry): %v",
				row.PluginName, row.ListingID, row.Version, err)
			converged = false
			continue
		}
		logging.L().Infof("plugin sync: installed %s (%s@%s) to follow the primary",
			row.PluginName, row.ListingID, row.Version)
	}

	// Uninstalls: a listing this till holds as active that the primary no
	// longer has. Keyed off local plugin_install_status records, so only
	// marketplace-installed plugins can ever be pruned (scope boundary
	// above). NOTE: a record stays State=Active while its plugin is merely
	// BROKEN (installed but unloadable, plugins.install_state='broken') —
	// the install lifecycle and load health are separate facts, and
	// wasm Sync's markBroken must never demote the record to Failed, or this
	// loop skips the plugin forever and it can never be uninstalled from a
	// replica (round-2 review BLOCKER; replicas reject manual uninstall).
	records, err := statusStore.List(ctx)
	if err != nil {
		logging.L().Errorf("plugin sync: list local install records: %v", err)
		converged = false
		records = nil
	}
	for listingID, rec := range records {
		if inPrimary[listingID] || rec.State != plugins.InstallStateActive || rec.PluginID == "" {
			continue
		}
		if _, err := cloudRemovePlugin(ctx, d, rec.PluginID); err != nil {
			logging.L().Warnf("plugin sync: uninstall %s (removed on the primary) failed (will retry): %v",
				rec.PluginID, err)
			converged = false
			continue
		}
		// A plugin pruned while in the broken-re-fetch backoff would
		// otherwise leave its stale attempt counter behind forever — the
		// healthy-reconvergence path is the only other place that clears it.
		clearBrokenRefetch(d, listingID)
		logging.L().Infof("plugin sync: uninstalled %s to follow the primary", rec.PluginID)
	}
	return converged
}

// shouldRefetchBroken reports whether this tick may attempt a marketplace
// re-fetch for a broken plugin, and does the attempt bookkeeping when it
// says yes. The first brokenRefetchMaxAttempts consecutive ticks attempt
// every time (covers the ordinary join-snapshot heal, which succeeds on
// attempt one); past that, only every brokenRefetchBackoffTicks-th tick
// attempts, and the first skipped tick logs — at error level, distinct from
// the per-attempt warn — that automatic recovery is not progressing.
// Counted per listing+version, in memory (Deps.BrokenRefetch); the counter
// clears when convergePluginSet later observes the plugin healthy, or when
// the primary moves the listing to a different version.
func shouldRefetchBroken(d *common.Deps, listingID, version string) bool {
	d.BrokenRefetchMu.Lock()
	defer d.BrokenRefetchMu.Unlock()
	if d.BrokenRefetch == nil {
		d.BrokenRefetch = map[string]*common.BrokenRefetchState{}
	}
	st := d.BrokenRefetch[listingID]
	if st == nil || st.Version != version {
		st = &common.BrokenRefetchState{Version: version}
		d.BrokenRefetch[listingID] = st
	}
	if st.Attempts < brokenRefetchMaxAttempts {
		st.Attempts++
		return true
	}
	st.TicksSkipped++
	if st.TicksSkipped >= brokenRefetchBackoffTicks {
		st.TicksSkipped = 0
		st.Attempts++ // stays past the cap: the slow cadence continues
		return true
	}
	if st.TicksSkipped == 1 {
		logging.L().Errorf("plugin sync: %s@%s is still broken after %d re-fetch attempts — automatic recovery is not progressing (a re-fetch can't fix a binary that can't load on this device); backing off to one attempt every %d ticks. Fix the cause (e.g. publish/install a compatible version on the primary) or re-install manually on the primary; restarting this till also resets the backoff",
			listingID, version, st.Attempts, brokenRefetchBackoffTicks)
	}
	return false
}

// clearBrokenRefetch forgets a listing's re-fetch attempt count once the
// plugin is observed installed-and-healthy at the primary's version.
func clearBrokenRefetch(d *common.Deps, listingID string) {
	d.BrokenRefetchMu.Lock()
	defer d.BrokenRefetchMu.Unlock()
	delete(d.BrokenRefetch, listingID)
}
