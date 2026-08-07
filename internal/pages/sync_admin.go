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
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
				"isReplica":  true,
				"class":      class,
				"label":      label,
				"queued":     queued,
				"offline":    !fresh,
				"primaryURL": primary,
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
		httpx.RenderPartial("ui/partials/sync_chip.html", map[string]any{
			"isReplica": false,
			"class":     class,
			"count":     len(list),
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
	_ = d.Settings.Set(ctx, "sync.last_contact_at", now)
	// Files ride alongside the row data: item images can change
	// without moving the admin fingerprint, so this runs every tick
	// (the manifest is cheap; only missing/changed files download).
	syncItemAssets(ctx, client, primary, bearer)
	if !out.Data.Unchanged {
		if err := adminRepo.ApplyAdmin(ctx, out.Data.Bundle); err != nil {
			logging.L().Errorf("sync pull: apply failed: %v", err)
			return
		}
		_ = d.Settings.Set(ctx, "sync.pull_version", out.Data.Version)
		_ = d.Settings.Set(ctx, "sync.last_pull_at", now)
		_ = posRepo.InsertAudit(ctx, nil, "system", "till", get("sync.till_id"), "admin_pulled",
			map[string]any{"version": out.Data.Version}, now, "")
		refresh(ctx)
		logging.L().Infof("sync pull: admin state %s applied from the primary", out.Data.Version)
	}

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
