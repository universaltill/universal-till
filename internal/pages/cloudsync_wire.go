package pages

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
	"github.com/universaltill/universal-till/internal/pos"
)

// StartCloudSync wires the ADR-0018 directive hooks to the till's real
// action paths and starts the cloud sync loop. Every hook is the same move
// an operator makes locally — remote installs still go through the
// download-token + Ed25519 verification path, remote settings through the
// same store + state re-derive as the settings pages.
func StartCloudSync(ctx context.Context, d *common.Deps, rederive func(context.Context)) {
	hooks := cloudsync.Hooks{
		SetSetting: func(ctx context.Context, key, value string) (string, error) {
			if err := d.Settings.Set(ctx, key, value); err != nil {
				return "", err
			}
			if rederive != nil {
				rederive(ctx)
			}
			return key + " = " + value, nil
		},
		InstallPlugin: func(ctx context.Context, listingID string) (string, error) {
			return cloudInstallPlugin(ctx, d, listingID)
		},
		RemovePlugin: func(ctx context.Context, pluginID string) (string, error) {
			return cloudRemovePlugin(ctx, d, pluginID)
		},
		// Remote price edit (cloud Catalog table): the same single-column
		// update a local price change makes; the next snapshot push (hash
		// gate) reflects it back to the cloud automatically.
		SetPrice: func(ctx context.Context, itemID string, priceMinor int64) (string, error) {
			if err := data.NewCatalogRepo(d.Db).SetItemPrice(ctx, itemID, priceMinor); err != nil {
				return "", err
			}
			return fmt.Sprintf("price set to %d (minor units)", priceMinor), nil
		},
		RenameItem: func(ctx context.Context, itemID, name string) (string, error) {
			if err := data.NewCatalogRepo(d.Db).SetItemName(ctx, itemID, name); err != nil {
				return "", err
			}
			return "renamed to " + name, nil
		},
		// Remote stock adjustment: the same movement record + connector event
		// a manual adjustment on the inventory page makes.
		AdjustStock: func(ctx context.Context, itemID string, delta float64, reason string) (string, error) {
			return cloudAdjustStock(ctx, d, itemID, delta, reason)
		},
		// The cloud's Design picker offers exactly what this till could pick
		// locally (built-in + plugin-contributed themes); applying one comes
		// back as a plain `set_setting theme` directive.
		DeviceExtra: func(ctx context.Context) map[string]any {
			themes := []map[string]string{}
			for _, opt := range availableThemes(ctx, d) {
				themes = append(themes, map[string]string{"key": opt.Key, "label": opt.Label})
			}
			return map[string]any{
				"theme":    d.CurrentState().Theme,
				"themes":   themes,
				"problems": collectProblems(ctx, d),
			}
		},
	}
	cloudsync.Start(ctx, d.Cfg, d.Db, hooks)
}

// cloudInstallPlugin mirrors handleInstallFromMarketplace for a directive:
// same installer, same signature verification, same install-status records.
func cloudInstallPlugin(ctx context.Context, d *common.Deps, listingID string) (string, error) {
	statusStore := plugins.NewInstallStatusStore(d.Db)
	_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
		ListingID: listingID,
		State:     plugins.InstallStateRequested,
	})
	effCfg := enroll.EnsureRegistered(ctx, d.Cfg, d.Settings)
	client := marketplace.NewClient(&effCfg.Marketplace, oauth.NewTokenClient(&effCfg.Marketplace))
	installer, err := plugins.NewMarketplaceInstaller(&effCfg, client, d.Db)
	if err != nil {
		_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
			ListingID: listingID, State: plugins.InstallStateFailed,
			MessageKey: "plugins.install.error.configuration",
		})
		return "", err
	}
	result, err := installer.Install(ctx, plugins.MarketplaceInstallRequest{
		ListingID:  listingID,
		MerchantID: effCfg.Marketplace.ClientID,
		StoreID:    effCfg.Marketplace.StoreID,
		DeviceID:   marketplace.DeviceIDFromConfig(&effCfg.Marketplace),
		DeviceArch: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		OnStateChange: func(state plugins.InstallLifecycleState) {
			_ = statusStore.Save(ctx, plugins.InstallStatusRecord{ListingID: listingID, State: state})
		},
	})
	if err != nil {
		failure := plugins.ClassifyInstallError(err)
		_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
			ListingID: listingID, State: plugins.InstallStateFailed,
			MessageKey: failure.MessageKey, Retryable: failure.Retryable,
		})
		return "", err
	}
	_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
		ListingID: listingID, PluginID: result.PluginID, PluginName: result.Name,
		CurrentVersion: result.Version, State: plugins.InstallStateActive,
	})
	if err := d.Pm.Reload(ctx); err != nil {
		log.Printf("Warning: failed to reload plugin manager: %v", err)
	}
	d.Menu = common.BuildMenu(d.BaseMenu, d.Pm)
	return "installed " + result.Name + " " + result.Version, nil
}

// cloudRemovePlugin mirrors handleUninstallPlugin for a directive.
func cloudRemovePlugin(ctx context.Context, d *common.Deps, pluginID string) (string, error) {
	if strings.ContainsAny(pluginID, `/\`) || strings.Contains(pluginID, "..") {
		return "", fmt.Errorf("invalid plugin id")
	}
	if err := plugins.UninstallPlugin(ctx, d.Db, pluginID); err != nil {
		return "", err
	}
	if err := os.RemoveAll(filepath.Join(paths.Plugins(), pluginID)); err != nil {
		log.Printf("warning: failed to remove plugin files %s: %v", pluginID, err)
	}
	if err := plugins.NewInstallStatusStore(d.Db).ClearForPlugin(ctx, pluginID); err != nil {
		log.Printf("warning: failed to clear install status for %s: %v", pluginID, err)
	}
	if d.Pm != nil {
		if err := d.Pm.Reload(ctx); err != nil {
			log.Printf("warning: failed to reload plugin manager after uninstall %s: %v", pluginID, err)
		}
		d.Menu = common.BuildMenu(d.BaseMenu, d.Pm)
	}
	return "uninstalled " + pluginID, nil
}

// collectProblems builds the heartbeat's problems digest: recent warn/error
// log lines from this process plus any failed plugin installs. Newest first,
// capped — it is a digest for the cloud's Problems feed, not a log shipper.
func collectProblems(ctx context.Context, d *common.Deps) []map[string]any {
	const maxProblems = 20
	out := []map[string]any{}
	for _, p := range logging.Recent() {
		if len(out) >= maxProblems {
			break
		}
		msg := p.Msg
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		out = append(out, map[string]any{
			"at": p.At.Format(time.RFC3339), "level": p.Level, "msg": msg,
		})
	}
	if records, err := plugins.NewInstallStatusStore(d.Db).List(ctx); err == nil {
		for _, rec := range records {
			if rec.State != plugins.InstallStateFailed || len(out) >= maxProblems {
				continue
			}
			name := rec.PluginName
			if name == "" {
				name = rec.ListingID
			}
			out = append(out, map[string]any{
				"at": rec.UpdatedAt, "level": "ERROR",
				"msg": "plugin install failed: " + name + " (" + rec.MessageKey + ")",
			})
		}
	}
	return out
}

// cloudAdjustStock mirrors the inventory page's manual adjustment for a
// directive: same stock-movement record, same connector event. The cloud has
// no location picker, so the movement lands where the item already tracks
// stock (else the shop's first stock location).
func cloudAdjustStock(ctx context.Context, d *common.Deps, itemID string, delta float64, reason string) (string, error) {
	locationID := ""
	if levels, err := data.NewPOSRepo(d.Db).ListStockLevels(ctx); err == nil {
		for _, l := range levels {
			if l.ItemID == itemID {
				locationID = l.LocationID
				break
			}
		}
	}
	if locationID == "" {
		if locs, err := data.NewCatalogRepo(d.Db).ReadLookup(ctx, "stock_locations"); err == nil && len(locs) > 0 {
			locationID = locs[0].ID
		}
	}
	if locationID == "" {
		return "", fmt.Errorf("no stock location configured")
	}
	if reason == "" {
		reason = "cloud adjustment"
	}
	if _, err := pos.RecordStockMovement(ctx, d.Db, pos.StockMovementInput{
		ItemID: itemID, LocationID: locationID, Type: "adjust",
		Quantity: delta, Reason: reason, ActorID: "cloud",
	}); err != nil {
		return "", err
	}
	publishStockAdjusted(ctx, d, plugins.StockAdjustedEvent{
		ItemID: itemID, DeltaQty: delta,
		Reason: stockMovementReason("adjust"), Location: locationID,
	})
	return fmt.Sprintf("stock adjusted by %+g", delta), nil
}
