package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
// harness of the journal push and the admin pull.
func runSyncLoop(ctx context.Context, tick func()) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tick()
			}
		}
	}()
}

// StartSyncPull runs the replica-side drift loop: every 30s fetch the
// primary's admin bundle and apply it when the fingerprint moved. refresh
// re-derives in-memory state (theme, tax engine, i18n) after an apply.
func StartSyncPull(ctx context.Context, d *common.Deps, refresh func(context.Context)) {
	adminRepo := data.NewSyncAdminRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	client := &http.Client{Timeout: 60 * time.Second}
	runSyncLoop(ctx, func() {
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
		if out.Data.Unchanged {
			return
		}
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
	})
}
