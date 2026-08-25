package pages

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/catalogtypes"
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
func StartCloudSync(ctx context.Context, d *common.Deps, rederive func(context.Context), wg *sync.WaitGroup) {
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
		// Create from the cloud. Idempotent on retry (same-name active item →
		// success without a duplicate); a taken barcode fails cleanly.
		CreateItem: func(ctx context.Context, name string, priceMinor int64, barcode string) (string, error) {
			return cloudCreateItem(ctx, d, name, priceMinor, barcode)
		},
		// Attach a (primary) barcode to an item or variant. AddBarcode owns
		// all the safety: availability, existence, active-only.
		AddBarcode: func(ctx context.Context, id, barcode string) (string, error) {
			repo := data.NewCatalogRepo(d.Db)
			in := catalogtypes.BarcodeInput{Barcode: barcode, IsPrimary: true}
			if exists, err := repo.ItemExists(ctx, id); err == nil && exists {
				in.ItemID = id
			} else {
				in.VariantID = id
			}
			if err := repo.AddBarcode(ctx, in); err != nil {
				return "", err
			}
			return "barcode " + barcode + " attached", nil
		},
		// Retire from the cloud: same soft-deactivate a manager does locally
		// (variants of the item retire with it; a variant id retires just the
		// variant). It drops from the sale screen and the next snapshot.
		DeactivateItem: func(ctx context.Context, id string) (string, error) {
			repo := data.NewCatalogRepo(d.Db)
			if exists, err := repo.ItemExists(ctx, id); err == nil && exists {
				if err := repo.DeactivateItem(ctx, id); err != nil {
					return "", err
				}
				return "item deactivated", nil
			}
			if _, ok, _ := repo.GetVariantLabel(ctx, id); !ok {
				return "", fmt.Errorf("item not found")
			}
			if err := repo.DeactivateVariant(ctx, id); err != nil {
				return "", err
			}
			return "variant deactivated", nil
		},
		// Remote stock adjustment: the same movement record + connector event
		// a manual adjustment on the inventory page makes.
		AdjustStock: func(ctx context.Context, itemID string, delta float64, reason string) (string, error) {
			return cloudAdjustStock(ctx, d, itemID, delta, reason)
		},
		// fiscal_tse_ready (ADR-0053, ut-docs#802): the cloud finished
		// reseller-provisioning this shop's TSE — fetch the operational
		// credential once (single-use endpoint) and store it at rest;
		// fiscal.tse_configured flips true only on confirmed local receipt.
		FiscalTSEReady: func(ctx context.Context) (string, error) {
			return applyFiscalTSEReady(ctx, d)
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
	cloudsync.Start(ctx, d.Cfg, d.Db, hooks, wg)
}

// cloudInstallPlugin mirrors handleInstallFromMarketplace for a directive:
// same installer, same signature verification, same install-status records,
// same reload-and-rebuild-menu tail. Cloud directives carry no version, so
// this installs the marketplace's current release — historical behavior,
// unchanged.
func cloudInstallPlugin(ctx context.Context, d *common.Deps, listingID string) (string, error) {
	return cloudInstallPluginVersion(ctx, d, listingID, "")
}

// cloudInstallPluginVersion is cloudInstallPlugin with an optional version
// pin. The LAN plugin sync (syncPullPlugins, ut-docs#460) passes the
// PRIMARY's recorded version so a replica converges to the version the shop
// actually runs — not whatever the marketplace happens to serve as latest
// (a primary pinned behind latest would otherwise silently fork its
// replicas). version=="" keeps the unpinned latest-release behavior.
func cloudInstallPluginVersion(ctx context.Context, d *common.Deps, listingID, version string) (string, error) {
	statusStore := plugins.NewInstallStatusStore(d.Db)

	// One read of the listing's existing record feeds two protections below:
	// the ut-docs#495 prior-good snapshot for pinned upgrades, and the
	// failed-attempt handling (ut-docs#368 second review round) that must
	// know whether a successfully-installed plugin is already on record.
	prior, hadPrior, priorErr := statusStore.Get(ctx, listingID)
	priorInstalled := priorErr == nil && hadPrior &&
		prior.State == plugins.InstallStateActive && prior.PluginID != ""

	// saveFailed records a failed install attempt. When this listing already
	// has a successfully-installed plugin on record (a pinned upgrade or a
	// broken-plugin re-fetch, not a fresh install), the record stays ACTIVE,
	// carrying the prior install's identity and version: the failed attempt
	// uninstalled nothing — that plugin is still on this till — and a record
	// demoted to Failed (or one whose blank PluginID clobbered the stored
	// one) is invisible to convergePluginSet's prune loop, which only prunes
	// Active records with a non-blank PluginID. That made a plugin whose
	// re-fetch keeps failing permanently un-removable from a replica even
	// after the shop owner removed it on the primary (ut-docs#368 second
	// review round BLOCKER — the same failure mode markBroken's round-1 fix
	// closed, via a second route). The failure MessageKey still lands on the
	// record so the attempt's outcome stays visible, mirroring the
	// rolled-back version-mismatch branch below.
	saveFailed := func(messageKey string, retryable bool) {
		rec := plugins.InstallStatusRecord{
			ListingID: listingID, State: plugins.InstallStateFailed,
			MessageKey: messageKey, Retryable: retryable,
		}
		if priorInstalled {
			rec.State = plugins.InstallStateActive
			rec.PluginID = prior.PluginID
			rec.PluginName = prior.PluginName
			rec.CurrentVersion = prior.CurrentVersion
		}
		_ = statusStore.Save(ctx, rec)
	}

	// ut-docs#495: a pinned (upgrade) install can fail the version-mismatch
	// check below, by which point Install has already overwritten this
	// listing's plugins-table row. Capture "the version that was good before
	// this attempt" NOW, while it's still the one on record, and snapshot
	// its files — the only way a later Rollback (instead of a full
	// uninstall) has anything to restore. Only pinned installs can ever hit
	// the mismatch branch, so unpinned (version == "") installs skip this
	// entirely.
	var priorGood plugins.InstallStatusRecord
	var hasPriorGood bool
	if version != "" && priorInstalled && prior.CurrentVersion != "" {
		priorGood = prior
		hasPriorGood = true
		sourcePath := filepath.Join(paths.Plugins(), prior.PluginID, prior.CurrentVersion)
		if err := plugins.NewRollbackManager(d.Db, paths.Plugins()).StoreVersion(prior.PluginID, prior.CurrentVersion, sourcePath); err != nil {
			logging.L().Warnf("plugin sync: failed to snapshot %s@%s before pinned install (rollback to it won't be possible if this mismatches): %v",
				prior.PluginID, prior.CurrentVersion, err)
			// Don't rely on Rollback's own os.Stat failure as the safety
			// net for a snapshot that was never actually taken — that
			// safety is accidental, living in the callee, not a decision
			// made here.
			hasPriorGood = false
		}
	}

	_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
		ListingID: listingID,
		State:     plugins.InstallStateRequested,
	})
	effCfg := enroll.EnsureRegistered(ctx, d.Cfg, d.Settings)
	client := marketplace.NewClient(&effCfg.Marketplace, oauth.NewTokenClient(&effCfg.Marketplace))
	installer, err := plugins.NewMarketplaceInstaller(&effCfg, client, d.Db)
	if err != nil {
		saveFailed("plugins.install.error.configuration", false)
		return "", err
	}
	result, err := installer.Install(ctx, plugins.MarketplaceInstallRequest{
		ListingID:  listingID,
		Version:    version,
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
		saveFailed(failure.MessageKey, failure.Retryable)
		return "", err
	}
	// ut-docs#479: a pinned request (version != "") must come back AS that
	// version. installer.Install succeeding proves the bundle verified
	// (signature, checksum, compatibility) — it says nothing about whether
	// the marketplace actually honored the pin. Today's marketplace
	// hard-errors on an unknown version instead of substituting one, so this
	// is defense in depth, not a live bug — but a future/different backend
	// answering the pin with the wrong release must not be accepted as a
	// silent success (a replica converging to the wrong plugin version on a
	// money-affecting path, e.g. tax, would be invisible).
	//
	// By this point Install has ALREADY persisted the wrong version: files
	// in place, plugins/plugin_catalog rows written, permissions granted
	// (installBundleFile / PersistManifest). A status-table flag alone
	// (round 1 of this fix) doesn't undo any of that — Manager.Reload reads
	// the plugins table with no version filtering, so the very next reload
	// (fired by ANY other install/uninstall later in this same tick, or a
	// later admin action) would wire the wrong, mismatched version into the
	// live menu/WASM runtime regardless. So roll it back completely, the
	// same way a real uninstall does, before reporting the failure — never
	// leave an unrequested version "installed and active" behind the scenes.
	//
	// Fixed (ut-docs#495): `plugins` is one row per plugin ID, and Install
	// already overwrote it before this check runs — so if this listing had a
	// DIFFERENT, previously-good version installed (an in-place upgrade
	// attempt that mismatched, not a fresh install), a plain cloudRemovePlugin
	// here would remove the WHOLE plugin directory tree — every version's
	// files, not just the bad one (os.RemoveAll targets pluginBaseDir/
	// pluginID, the parent of every per-version subdirectory) — on top of
	// dropping its plugins/plugin_catalog rows and permissions. If the
	// snapshot above captured a prior good version for this exact plugin,
	// restore it via RollbackManager.Rollback instead: the old version stays
	// installed and active, and only this failed upgrade attempt is reported
	// as failed. Only a fresh install (nothing to restore) still gets the
	// full cloudRemovePlugin — unchanged from before this fix — and so does
	// an upgrade whose Rollback itself fails (never leave a half-migrated
	// plugin behind silently).
	if version != "" && result.Version != version {
		rolledBack := false
		switch {
		case hasPriorGood && priorGood.PluginID == result.PluginID && result.Version == priorGood.CurrentVersion:
			// The marketplace answered the pin with a version that mismatches
			// the PIN but happens to already BE the prior good version (e.g.
			// re-served instead of a genuinely new release) — Install has
			// already re-persisted it correctly. Nothing to restore:
			// RollbackManager.Rollback would just error "already at version"
			// (rollback.go) and fall through to a needless full uninstall of
			// an already-correct install.
			rolledBack = true
		case hasPriorGood && priorGood.PluginID == result.PluginID:
			if rbErr := plugins.NewRollbackManager(d.Db, paths.Plugins()).Rollback(ctx, result.PluginID, priorGood.CurrentVersion, "system"); rbErr != nil {
				logging.L().Errorf("plugin sync: failed to roll back %s to prior version %s after mismatch, falling back to full uninstall: %v",
					result.Name, priorGood.CurrentVersion, rbErr)
			} else {
				rolledBack = true
				if err := d.ReloadPlugins(ctx); err != nil {
					logging.L().Warnf("plugin sync: failed to reload plugin manager after rolling back %s: %v", result.Name, err)
				}
				// The mismatched version's own directory is now orphaned —
				// Rollback only touches DB rows, and nothing else ever cleans
				// up a per-version install dir that no row points at anymore.
				// Left alone, this and every future retry against the same
				// still-mismatching pin would leak disk space forever.
				if rmErr := os.RemoveAll(filepath.Join(paths.Plugins(), result.PluginID, result.Version)); rmErr != nil {
					logging.L().Warnf("plugin sync: failed to remove orphaned mismatched-version files for %s@%s: %v", result.PluginID, result.Version, rmErr)
				}
			}
		}
		if !rolledBack {
			if _, rmErr := cloudRemovePlugin(ctx, d, result.PluginID); rmErr != nil {
				logging.L().Errorf("plugin sync: failed to roll back mismatched install of %s (%s): %v", result.Name, result.PluginID, rmErr)
			}
		}
		rec := plugins.InstallStatusRecord{
			ListingID: listingID, PluginID: result.PluginID, PluginName: result.Name,
			MessageKey: "plugins.install.error.version_mismatch", Retryable: true,
		}
		if rolledBack {
			// The plugin is genuinely still installed and ACTIVE at the prior
			// good version — this record must say so, not Failed, or two
			// things break: (1) the NEXT tick's "is there a prior good
			// version to protect" check above reads this same record and
			// requires State==Active, so a persistently-mismatching pin
			// would lose the protection on the very next retry and fully
			// uninstall anyway; (2) convergePluginSet's prune loop only ever
			// prunes an Active record, so a Failed record here would make
			// this plugin permanently unprunable even after the primary
			// legitimately removes the listing later.
			rec.State = plugins.InstallStateActive
			rec.CurrentVersion = priorGood.CurrentVersion
		} else {
			rec.State = plugins.InstallStateFailed
		}
		_ = statusStore.Save(ctx, rec)
		return "", fmt.Errorf("installed version %s does not match requested version %s", result.Version, version)
	}
	_ = statusStore.Save(ctx, plugins.InstallStatusRecord{
		ListingID: listingID, PluginID: result.PluginID, PluginName: result.Name,
		CurrentVersion: result.Version, State: plugins.InstallStateActive,
	})
	// ReloadPlugins nil-checks d.Pm (this path used to dereference it bare
	// while cloudRemovePlugin checked — now both are safe) and serializes
	// the reload + menu rebuild against every other lifecycle call site.
	if err := d.ReloadPlugins(ctx); err != nil {
		log.Printf("Warning: failed to reload plugin manager: %v", err)
	}
	return "installed " + result.Name + " " + result.Version, nil
}

// cloudRemovePlugin mirrors handleUninstallPlugin for a directive: DB rows,
// installed files, install-status records, then the shared
// reload-and-rebuild-menu tail. Also the uninstall step of the LAN plugin
// sync (syncPullPlugins, ut-docs#460).
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
	if err := d.ReloadPlugins(ctx); err != nil {
		log.Printf("warning: failed to reload plugin manager after uninstall %s: %v", pluginID, err)
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
			// Walk back to a rune boundary so we never split a multi-byte
			// character mid-sequence. Assumes msg is already valid UTF-8 (as
			// logged strings are); it doesn't sanitize already-malformed input.
			cut := 200
			for cut > 0 && !utf8.RuneStart(msg[cut]) {
				cut--
			}
			msg = msg[:cut] + "…"
		}
		out = append(out, map[string]any{
			// Nanosecond precision, not RFC3339's whole-second: the cloud
			// dedupes persisted problem history on (device, at, msg), and a
			// fast repeat of the same message within one second would
			// otherwise collide and get dropped as a false duplicate.
			"at": p.At.Format(time.RFC3339Nano), "level": p.Level, "msg": msg,
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

// cloudCreateItem creates a catalog item from a directive. Directives are
// at-least-once, so a retry must not duplicate: an existing active item with
// the same name counts as success. A barcode already attached elsewhere
// fails the directive (visible in the result column) rather than stealing it.
func cloudCreateItem(ctx context.Context, d *common.Deps, name string, priceMinor int64, barcode string) (string, error) {
	repo := data.NewCatalogRepo(d.Db)
	if _, exists, err := repo.FindActiveItemByName(ctx, name); err == nil && exists {
		return "item already exists", nil
	}
	if barcode != "" {
		if taken, err := repo.BarcodeExists(ctx, barcode); err == nil && taken {
			return "", fmt.Errorf("barcode %s is already in use", barcode)
		}
	}
	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		Name: name, BasePrice: priceMinor, IsActive: true,
	})
	if err != nil {
		return "", err
	}
	if barcode != "" {
		if err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{
			ItemID: id, Barcode: barcode, IsPrimary: true,
		}); err != nil {
			var conflict *data.BarcodeConflictError
			if errors.As(err, &conflict) {
				// Same fix as the catalog/import UUID leak (ut-docs#303):
				// name the conflicting item/variant instead of its raw
				// internal ID in this directive-result text (reported
				// back to the cloud dashboard). "en" — this whole hooks
				// struct's result strings are operational/audit text, not
				// shop-floor UI, so unlike import_page.go they're
				// deliberately not routed through the shop's own locale.
				return "created " + name + " (barcode not attached: " + common.FriendlyBarcodeConflict(ctx, repo, "en", err) + ")", nil
			}
			// Not a conflict — unlike import_page.go's operator-facing
			// text, this string's only reader is a developer/admin on the
			// cloud dashboard, so the real error stays (ut-docs#303
			// review: genericizing this too made non-conflict failures
			// undiagnosable from the cloud side, a real regression).
			return "created " + name + " (barcode not attached: " + err.Error() + ")", nil
		}
	}
	return "created " + name, nil
}
