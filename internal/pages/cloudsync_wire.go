package pages

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/diagnostics"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
	"github.com/universaltill/universal-till/internal/pos"
)

// rejectRemoteFiscalPostureWrite refuses key when it names either of the
// two signing-device posture settings — the flat wire name or an explicit
// per-country row — for the cloud SetSetting directive specifically
// (ADR-0083, ut-docs#1767, independent review finding).
//
// Both keys are owner-only everywhere else: settings_page.go's upsert/save
// handlers both gate them behind canPerform("fiscal_tse_override"),
// because ADR-0048 treats them as a compliance-critical flag, not ordinary
// shop config. This directive has no HTTP session to check a role
// against, so — same as the country-change guard in SetSetting below,
// which also takes its fail-closed half unconditionally because nobody
// here can authorize the other half — it refuses these two names outright
// rather than silently writing them (or, worse, silently NOT writing: the
// per-country split would otherwise have turned a remote "set false"
// revocation into a no-op that still reported success, because the flat
// wire name resolves to a real per-country row everywhere else but this
// handler used to write it verbatim as a dead, un-suffixed key — fail-open
// in exactly the direction that matters, since the till keeps selling on
// the stale "true" row).
func rejectRemoteFiscalPostureWrite(d *common.Deps, key string) error {
	logicalKey, _ := resolveFiscalPostureKey(d, key)
	if logicalKey == wireKeySigningDeviceConfigured || logicalKey == wireKeySigningDeviceFailingSince {
		return fmt.Errorf("fiscal posture (%s) is owner-only and cannot be set via a remote directive", logicalKey)
	}
	return nil
}

// allowedRemoteTillSettingKeys is the till-side whitelist for the
// set_till_setting directive (ut-docs#2289; Decision 1 of the shared
// portal-till configuration design in universaltill/ut-docs#2306, proposed
// as ADR-0095 — that PR is still open, so every citation in this slice
// names the issue rather than an ADR number not yet accepted on ut-docs'
// main; swap them for "ADR-0095" once it merges): the safe,
// non-device-bound shop-config keys the cloud panel may write. It MUST match
// ut-cloud's claims.AllowedTillSettingKeys byte-for-byte — the portal
// filters too, but this list is the one that holds when the portal is
// stale or wrong, which is exactly why it's re-checked here rather than
// trusted. Built from the same constants the till's own settings pages
// write (print_api.go, common.KeyKioskIdleReset), never re-typed strings,
// so it can't drift from what those pages store.
//
// Never on this list, by decision: printer addresses/device paths,
// payment-terminal pairing, fiscal/TSE credentials or posture, PINs, network
// or sync topology, store.country (its fiscal side-effects are
// SetSetting's job). Intentionally extensible: order-type prompt placement,
// the Categories-tab toggle and the order-number scheme are expected
// follow-ups once those settings exist (concurrent, unmerged work).
var allowedRemoteTillSettingKeys = map[string]bool{
	keyPrinterReceiptPolicy:  true,
	keyReceiptHeader1:        true,
	keyReceiptHeader2:        true,
	keyReceiptHeader3:        true,
	keyReceiptFooter:         true,
	common.KeyKioskIdleReset: true,
}

// cloudSetTillSetting is the set_till_setting hook: whitelist check, then the
// SAME value validation the local settings form for that key applies
// (ut-docs#2306's design: "the till still validates each payload exactly as
// it would the same action performed by hand"), then the write and the state
// re-derive the generic SetSetting hook also does (kiosk.idle_reset_seconds
// lives in the derived State, so without it the new window wouldn't apply
// until a restart). A refused key or value writes nothing and re-derives
// nothing.
func cloudSetTillSetting(ctx context.Context, d *common.Deps, rederive func(context.Context), key, value string) (string, error) {
	if !allowedRemoteTillSettingKeys[key] {
		return "", fmt.Errorf("%s is not a remote-configurable till setting", key)
	}
	value = strings.TrimSpace(value)
	switch key {
	case keyPrinterReceiptPolicy:
		// Mirrors /api/settings/printer (print_api.go): case-folded, must be
		// one of the three values, permitted by the installed country plugin
		// (ADR-0089 Decision 2). The former DE-only lock (Decision 3) was
		// removed core-wide by ut-docs#2286/universal-till#1188 — German
		// shops now choose freely like every other country, so this hook has
		// no country-specific branch left to mirror. Unlike the local form,
		// an unknown value is refused rather than silently defaulted — a
		// remote result column should say why nothing changed.
		value = strings.ToLower(value)
		if !isReceiptPolicy(value) {
			return "", fmt.Errorf("%s must be one of always, ask, never", key)
		}
		if allowed, ok := receiptPolicyAskerFor(d.Db).AskReceiptPolicy(ctx); !receiptPolicyPermitted(value, allowed, ok) {
			return "", fmt.Errorf("%s is not permitted by the installed country plugin (allowed: %s)", key, strings.Join(allowed, ", "))
		}
	case common.KeyKioskIdleReset:
		// Mirrors /api/settings/kiosk-idle-reset (settings_page.go): 0..600.
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 600 {
			return "", fmt.Errorf("%s must be between 0 and 600 seconds", key)
		}
		value = strconv.Itoa(n)
	case keyReceiptHeader1, keyReceiptHeader2, keyReceiptHeader3, keyReceiptFooter:
		// Receipt header/footer lines: free text, trimmed, blank clears —
		// exactly what receipt_designer.go's save does.
	default:
		// Unreachable while this switch covers every whitelisted key — and
		// that is exactly the point (review of ut-docs#2289). A key added
		// to allowedRemoteTillSettingKeys without a decision about what its
		// local form validates lands here and is refused, instead of
		// silently inheriting the free-text treatment above. Fail closed:
		// the whitelist says which keys are remote-settable, this switch
		// says how each one is checked, and neither may grow without the
		// other.
		return "", fmt.Errorf("%s has no remote validation rule on this till", key)
	}
	if err := d.Settings.Set(ctx, key, value); err != nil {
		return "", err
	}
	if rederive != nil {
		rederive(ctx)
	}
	return key + " = " + value, nil
}

// remoteTillSettingsReport is the read side (ut-docs#2306 Decision 2,
// proposed ADR-0095, pending merge): the current value of every whitelisted
// key, unset ones as "", for the heartbeat's device record — the cloud's
// till-settings forms pre-fill from this so the merchant sees applied state,
// not just what was queued. Only whitelisted keys are ever reported; nothing
// else in the settings table rides along.
func remoteTillSettingsReport(ctx context.Context, d *common.Deps) map[string]string {
	out := make(map[string]string, len(allowedRemoteTillSettingKeys))
	for key := range allowedRemoteTillSettingKeys {
		v, _, _ := d.Settings.Get(ctx, key)
		out[key] = v
	}
	return out
}

// cloudSetQuickButtonLayout is the set_quick_button_layout hook: reorders
// the quick-sale (shortcut) buttons from the cloud's layout panel — the
// same ShortcutsRepo.UpdateOrder call the Designer's own move-up/move-down
// reorder makes locally via POST /api/buttons/reorder. Gated to the primary
// till (requirePrimaryDirective) for the same reason that LAN route is
// (ut-docs#1697): shortcut_buttons syncs shop-wide as a primary-wins admin
// table, so a write applied on a replica would vanish on the very next
// admin pull.
//
// buttons_api.go's own reorder route documents its payload as "the FULL
// global list" — UpdateOrder sets sort_order = index only for the barcodes
// it's given, so a PARTIAL list leaves the omitted rows on their old
// sort_order values, which can collide with a listed row's new one
// (verified: an independent review's probe reproduced a real duplicate
// sort_order from a partial payload, ut-docs#2321 review). A cloud
// directive's payload isn't validated against the till by anything but
// this function, so — unlike the LAN route, which trusts its own page to
// always post the full list — this rejects a payload that doesn't cover
// EXACTLY the till's current button set (missing or unknown barcodes
// both refused, named in the error) rather than silently applying a
// partial reorder.
func cloudSetQuickButtonLayout(ctx context.Context, d *common.Deps, barcodes []string) (string, error) {
	if len(barcodes) == 0 {
		return "", fmt.Errorf("missing barcodes")
	}
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	repo := data.NewShortcutsRepo(d.Db)
	current, err := repo.LoadButtons(ctx)
	if err != nil {
		return "", err
	}
	currentSet := make(map[string]bool, len(current))
	for _, b := range current {
		currentSet[b.Barcode] = true
	}
	given := make(map[string]bool, len(barcodes))
	for _, bc := range barcodes {
		if given[bc] {
			return "", fmt.Errorf("duplicate barcode %q in the new layout", bc)
		}
		given[bc] = true
		if !currentSet[bc] {
			return "", fmt.Errorf("barcode %q is not one of this till's quick buttons", bc)
		}
	}
	for bc := range currentSet {
		if !given[bc] {
			return "", fmt.Errorf("the new layout is missing barcode %q — it must list every current quick button", bc)
		}
	}
	if err := repo.UpdateOrder(ctx, barcodes); err != nil {
		return "", err
	}
	auditCloudDirective(ctx, d, "quick_buttons", "-", "quick_button_layout_set", map[string]any{"barcodes": barcodes})
	return fmt.Sprintf("layout applied to %d buttons", len(barcodes)), nil
}

// remoteQuickButtonsReport is the read side for DeviceExtra: the currently
// applied quick-sale button layout (barcode + label, in the same sort order
// LoadButtons itself orders by), so the cloud's layout panel shows applied
// state, not just what was queued — same "report what's actually there"
// pattern as remoteTillSettingsReport above. A read error reports an empty
// list rather than failing the whole heartbeat.
func remoteQuickButtonsReport(ctx context.Context, d *common.Deps) []map[string]any {
	buttons, err := data.NewShortcutsRepo(d.Db).LoadButtons(ctx)
	if err != nil {
		logging.L().Warnf("cloudsync: quick button layout report failed: %v", err)
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(buttons))
	for i, b := range buttons {
		// sort_order mirrors this button's position in the (already
		// sort_order-ordered) list LoadButtons returns — the cloud side
		// decodes it into QuickButtonReport.SortOrder so a single entry is
		// self-describing without its slice context (claims.go's own doc
		// comment on that field), even though the panel today only reads
		// the report's array order, not this field, to render the list.
		out = append(out, map[string]any{"barcode": b.Barcode, "label": b.Label, "sort_order": i})
	}
	return out
}

// StartCloudSync wires the ADR-0018 directive hooks to the till's real
// action paths and starts the cloud sync loop. Every hook is the same move
// an operator makes locally — remote installs still go through the
// download-token + Ed25519 verification path, remote settings through the
// same store + state re-derive as the settings pages.
func StartCloudSync(ctx context.Context, d *common.Deps, rederive func(context.Context), wg *sync.WaitGroup) {
	cloudsync.Start(ctx, d.Cfg, d.Db, buildCloudHooks(d, rederive), wg)
}

// buildCloudHooks is StartCloudSync's hook set, split out so tests can
// exercise the wiring (which hook handles which directive, what the device
// report carries) without starting the sync goroutine.
func buildCloudHooks(d *common.Deps, rederive func(context.Context)) cloudsync.Hooks {
	return cloudsync.Hooks{
		SetSetting: func(ctx context.Context, key, value string) (string, error) {
			if err := rejectRemoteFiscalPostureWrite(d, key); err != nil {
				return "", err
			}
			// ut-docs#1750: the third writer of store.country, and the one
			// with no HTTP caller to authorize — so it takes the
			// fail-closed half of the invariant unconditionally: a shop
			// moved to another market loses a fiscal posture that was only
			// ever proven for the old one. Before the write, so a failure
			// cannot leave the country moved with the posture still set.
			if key == common.KeyCountry {
				if err := clearFiscalStateForCountryChange(ctx, d, "", value); err != nil {
					return "", err
				}
			}
			if err := d.Settings.Set(ctx, key, value); err != nil {
				return "", err
			}
			if rederive != nil {
				rederive(ctx)
			}
			return key + " = " + value, nil
		},
		// set_till_setting (ut-docs#2289, design in ut-docs#2306): the
		// whitelisted, value-validated sibling of SetSetting above — see
		// cloudSetTillSetting for the list and the checks.
		SetTillSetting: func(ctx context.Context, key, value string) (string, error) {
			return cloudSetTillSetting(ctx, d, rederive, key, value)
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
			return cloudSetPrice(ctx, d, itemID, priceMinor)
		},
		RenameItem: func(ctx context.Context, itemID, name string) (string, error) {
			return cloudRenameItem(ctx, d, itemID, name)
		},
		// Create from the cloud. Idempotent on retry (same-name active item →
		// success without a duplicate); a taken barcode fails cleanly.
		CreateItem: func(ctx context.Context, name string, priceMinor int64, barcode string) (string, error) {
			return cloudCreateItem(ctx, d, name, priceMinor, barcode)
		},
		// Attach a (primary) barcode to an item or variant. AddBarcode owns
		// all the safety: availability, existence, active-only.
		AddBarcode: func(ctx context.Context, id, barcode string) (string, error) {
			return cloudAddBarcode(ctx, d, id, barcode)
		},
		// Retire from the cloud: same soft-deactivate a manager does locally
		// (variants of the item retire with it; a variant id retires just the
		// variant). It drops from the sale screen and the next snapshot.
		DeactivateItem: func(ctx context.Context, id string) (string, error) {
			return cloudDeactivateItem(ctx, d, id)
		},
		// Remote stock adjustment: the same movement record + connector event
		// a manual adjustment on the inventory page makes.
		AdjustStock: func(ctx context.Context, itemID string, delta float64, reason string) (string, error) {
			return cloudAdjustStock(ctx, d, itemID, delta, reason)
		},
		// fiscal_tse_ready (ADR-0053, ut-docs#802): the cloud finished
		// reseller-provisioning this shop's TSE — fetch the operational
		// credential once (single-use endpoint) and store it at rest;
		// fiscal.signing_device_configured flips true only on confirmed local receipt.
		FiscalTSEReady: func(ctx context.Context) (string, error) {
			return applyFiscalTSEReady(ctx, d)
		},
		// upsert_category (ut-docs#2323, ADR-0095 Decision 1): the cloud
		// panel's categories editor — same two repo calls categories_page.go
		// makes, same palette check. See cloudUpsertCategory.
		UpsertCategory: func(ctx context.Context, id, name, color string) (string, error) {
			return cloudUpsertCategory(ctx, d, id, name, color)
		},
		// update_item_details (ut-docs#2324, ADR-0095 Decision 1): the cloud
		// panel's item "More…" disclosure form — a partial update of sku/
		// description/unit/colour/is_weighed/stock_untracked. See
		// cloudUpdateItemDetails for the nil-means-untouched contract and
		// why category/brand/tax-code are excluded.
		UpdateItemDetails: func(ctx context.Context, itemID string, sku, description, unit, color *string, isWeighed, stockUntracked *bool) (string, error) {
			return cloudUpdateItemDetails(ctx, d, itemID, sku, description, unit, color, isWeighed, stockUntracked)
		},
		// upsert_modifier_group (ut-docs#2322, ADR-0095 Decision 1): the
		// cloud panel's per-item modifier-group creator — CREATE-ONLY, same
		// scope cut as UpsertCategory above. See cloudUpsertModifierGroup.
		UpsertModifierGroup: func(ctx context.Context, itemID, name string, required bool, minSelect, maxSelect int, options []cloudsync.ModifierGroupOption) (string, error) {
			return cloudUpsertModifierGroup(ctx, d, itemID, name, required, minSelect, maxSelect, options)
		},
		// diagnostic_mode_revoke (ADR-0092 §1/§4, ut-docs#2169): Universal
		// Till ended this till's diagnostic session — clear the local flag
		// and drain that session's whole pending queue in one step. Same
		// settings store the local stop control writes, so the two paths
		// can never disagree about what "off" means.
		DiagnosticModeRevoke: func(ctx context.Context, sessionID string) (string, error) {
			msg, err := diagnostics.Revoke(ctx, d.Settings, sessionID)
			if err == nil {
				auditDiagnostics(ctx, d, "system", "diagnostics_revoked", map[string]any{"session_id": sessionID})
			}
			return msg, err
		},
		// set_quick_button_layout: the cloud's quick-sale button layout
		// panel — same UpdateOrder call the Designer's own reorder makes,
		// gated to the primary till. See cloudSetQuickButtonLayout.
		SetQuickButtonLayout: func(ctx context.Context, barcodes []string) (string, error) {
			return cloudSetQuickButtonLayout(ctx, d, barcodes)
		},
		// The cloud's Design picker offers exactly what this till could pick
		// locally (built-in + plugin-contributed themes); applying one comes
		// back as a plain `set_setting theme` directive. cloudThemeOptions
		// resolves each label through the translator the same way the local
		// Settings <select> does (ut-docs#2015 review) — a plugin theme's
		// label is a translator key, so sending it raw would list
		// "theme.midnight.label" in the portal.
		DeviceExtra: func(ctx context.Context) map[string]any {
			themes := cloudThemeOptions(ctx, d)
			return map[string]any{
				"theme":    d.CurrentState().Theme,
				"themes":   themes,
				"problems": collectProblems(ctx, d),
				// ut-docs#2306 Decision 2 (proposed ADR-0095,
				// pending merge): the applied value of every
				// remote-configurable setting, for the portal's
				// forms.
				"till_settings": remoteTillSettingsReport(ctx, d),
				// The applied quick-sale button layout (barcode + label, in
				// sort order), for the cloud's layout panel to pre-fill from
				// real state rather than only what was queued. See
				// remoteQuickButtonsReport.
				"quick_buttons": remoteQuickButtonsReport(ctx, d),
			}
		},
	}
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
	// ut-docs#1370: same post-activation reconcile as
	// handleInstallFromMarketplace — a directive/sync install (or pinned
	// upgrade) of the German tax plugin folds the catalog's pinned takeaway
	// rates into takeaway_rate_overrides, add-only. Best-effort, logged.
	reconcileTaxDeTakeawayOverridesIfActivated(ctx, d.Db, result.PluginID)
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

// requirePrimaryDirective refuses a directive that would write to a table
// synced shop-wide via the primary-wins admin pull, matching the local
// admin path's requirePrimary gate (ut-docs#2353): items/item_variants/
// item_barcodes/variant_barcodes (catalog) and shortcut_buttons (quick-sale
// button layout, ut-docs#2321) are all admin-synced tables (primary-wins
// pull, sync_admin_repo.go's adminTables), so a write accepted here would
// silently vanish on the next admin pull. Directive hooks have no
// http.ResponseWriter to answer with an HTTP status, so this returns a
// plain error surfaced in the directive's result column instead — same
// shape as this file's other refusals (e.g. cloudCreateItem's barcode
// conflict).
func requirePrimaryDirective(ctx context.Context, d *common.Deps) error {
	if primary := d.SyncPrimaryURL(ctx); primary != "" {
		return fmt.Errorf("this data is primary-wins synced; make this change on the shop's primary till (%s)", primary)
	}
	return nil
}

// auditCloudDirective records a cloud-originated catalog mutation under the
// "system" actor (same FK-safety reasoning as cloudAdjustStock's own
// ActorID: "system", ut-docs#1676) so the audit trail can distinguish "an
// operator changed this at the till" from "the merchant changed this from
// the cloud portal" (ut-docs#2353). The insert's own error is logged, not
// discarded: for a mutation with no other audit trail on either the local
// or cloud path (e.g. item creation), a silently-dropped row would
// misattribute a cloud-originated change as an operator's own action —
// the opposite of this helper's purpose.
func auditCloudDirective(ctx context.Context, d *common.Deps, entityType, entityID, action string, payload any) {
	now := time.Now().UTC().Format(time.RFC3339)
	if err := data.NewPOSRepo(d.Db).InsertAudit(ctx, nil, "system", entityType, entityID, action, payload, now, ""); err != nil {
		log.Printf("[cloudsync] audit %s %s %s: %v", action, entityType, entityID, err)
	}
}

// cloudSetPrice is the set_price hook: same single-column update a local
// price change makes, primary-gated and audited like every other
// catalog-mutating directive (ut-docs#2353).
func cloudSetPrice(ctx context.Context, d *common.Deps, itemID string, priceMinor int64) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	if err := data.NewCatalogRepo(d.Db).SetItemPrice(ctx, itemID, priceMinor); err != nil {
		return "", err
	}
	auditCloudDirective(ctx, d, "item", itemID, "cloud_price_set", map[string]any{"price_minor": priceMinor})
	return fmt.Sprintf("price set to %d (minor units)", priceMinor), nil
}

// cloudRenameItem is the rename_item hook, primary-gated and audited like
// every other catalog-mutating directive (ut-docs#2353).
func cloudRenameItem(ctx context.Context, d *common.Deps, itemID, name string) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	if err := data.NewCatalogRepo(d.Db).SetItemName(ctx, itemID, name); err != nil {
		return "", err
	}
	auditCloudDirective(ctx, d, "item", itemID, "cloud_item_renamed", map[string]any{"name": name})
	return "renamed to " + name, nil
}

// cloudAddBarcode is the add_barcode hook: attaches a (primary) barcode to
// an item or variant. AddBarcode owns all the safety (availability,
// existence, active-only); this wrapper adds the primary gate and audit row
// every other catalog-mutating directive now has (ut-docs#2353).
func cloudAddBarcode(ctx context.Context, d *common.Deps, id, barcode string) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	repo := data.NewCatalogRepo(d.Db)
	in := catalogtypes.BarcodeInput{Barcode: barcode, IsPrimary: true}
	entityType := "item"
	if exists, err := repo.ItemExists(ctx, id); err == nil && exists {
		in.ItemID = id
	} else {
		in.VariantID = id
		entityType = "item_variant"
	}
	if err := repo.AddBarcode(ctx, in); err != nil {
		return "", err
	}
	auditCloudDirective(ctx, d, entityType, id, "cloud_barcode_added", map[string]any{"barcode": barcode})
	return "barcode " + barcode + " attached", nil
}

// cloudDeactivateItem is the deactivate_item hook: same soft-deactivate a
// manager does locally (variants of the item retire with it; a variant id
// retires just the variant), primary-gated and audited like every other
// catalog-mutating directive (ut-docs#2353).
func cloudDeactivateItem(ctx context.Context, d *common.Deps, id string) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	repo := data.NewCatalogRepo(d.Db)
	if exists, err := repo.ItemExists(ctx, id); err == nil && exists {
		if err := repo.DeactivateItem(ctx, id); err != nil {
			return "", err
		}
		auditCloudDirective(ctx, d, "item", id, "cloud_item_deactivated", nil)
		return "item deactivated", nil
	}
	if _, ok, _ := repo.GetVariantLabel(ctx, id); !ok {
		return "", fmt.Errorf("item not found")
	}
	if err := repo.DeactivateVariant(ctx, id); err != nil {
		return "", err
	}
	auditCloudDirective(ctx, d, "item_variant", id, "cloud_variant_deactivated", nil)
	return "variant deactivated", nil
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
		// audit_log.actor_id has a real FK to users(id) -- "cloud" was
		// never a seeded user, so every real call here violated it (the
		// old hand-rolled test schema had no such FK, which is why this
		// went uncaught -- ut-docs#1676). "system" is the established id
		// for this exact situation (see sync_orders.go's auditActorID).
		// The "cloud: " prefix keeps the adjustment's real origin visible
		// in the audit payload's reason field now that the actor itself
		// can no longer say so.
		Quantity: delta, Reason: "cloud: " + reason, ActorID: "system",
	}); err != nil {
		return "", err
	}
	publishStockAdjusted(ctx, d, plugins.StockAdjustedEvent{
		ItemID: itemID, DeltaQty: delta,
		Reason: stockMovementReason("adjust"), Location: locationID,
	})
	return fmt.Sprintf("stock adjusted by %+g", delta), nil
}

// cloudUpsertCategory is the upsert_category hook (ut-docs#2323, ADR-0095
// Decision 1): an empty id creates a category, a present one updates its
// name and colour — the same CreateCategoryWithColor / UpdateCategory calls
// categories_page.go's two POST handlers make, behind the same validation
// its parseCategoryForm applies (name required; colour must be one of
// catalogtypes.ItemColors()' fixed swatches or blank — a real allowlist, the
// value lands in a CSS custom property on the sale screen, see ItemColors'
// own doc comment). A refused colour writes nothing.
//
// Directives are at-least-once, so a retried CREATE must not duplicate: an
// existing category with the same name (case-insensitive, like the import
// path's EnsureCategory) counts as success and is left untouched — the
// retry is a no-op, not a silent recolour of a row the merchant may have
// edited locally since. A retried UPDATE is naturally idempotent.
//
// Modifier-group and kitchen-station links are deliberately NOT written
// here (the local dialog's saveCategoryLinks half): the portal can't offer
// a safe picker for them until the read-side snapshot carries the shop's
// groups/stations (ADR-0095 Decision 2, not yet shipped), so an id it sent
// today could only be a guess. Sort order, active flag and parent are
// untouched, exactly as UpdateCategory promises.
//
// categories is an admin-synced table, same as items — gated and audited
// the same way every other catalog-mutating directive now is
// (requirePrimaryDirective/auditCloudDirective, ut-docs#2353), matching
// the local admin category dialog's own requirePrimary gate + audit()
// call (categories_page.go). The gate runs after the idempotency
// short-circuit on create, same reasoning as cloudCreateItem: a replica
// replay against a category that already exists (the normal case, pulled
// down from the primary) should report success, not a spurious refusal.
func cloudUpsertCategory(ctx context.Context, d *common.Deps, id, name, color string) (string, error) {
	name = strings.TrimSpace(name)
	color = strings.TrimSpace(color)
	if !catalogtypes.ValidItemColor(color) {
		return "", fmt.Errorf("colour %q is not one of the category palette colours", color)
	}
	repo := data.NewCatalogRepo(d.Db)
	if id == "" {
		if name == "" {
			// UpdateCategory/CreateCategoryWithColor refuse this too; checked
			// here so the dedupe scan below never runs for a blank name.
			return "", data.ErrCategoryNameRequired
		}
		existing, err := repo.ListCategoriesForAdmin(ctx)
		if err != nil {
			return "", err
		}
		for _, c := range existing {
			if c.IsActive && strings.EqualFold(c.Name, name) {
				return "category " + c.Name + " already exists", nil
			}
		}
		if err := requirePrimaryDirective(ctx, d); err != nil {
			return "", err
		}
		newID, err := repo.CreateCategoryWithColor(ctx, name, color)
		if err != nil {
			return "", err
		}
		auditCloudDirective(ctx, d, "category", newID, "cloud_category_created", map[string]any{"name": name, "color": color})
		return "created category " + name, nil
	}
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	if err := repo.UpdateCategory(ctx, id, name, color); err != nil {
		return "", err
	}
	auditCloudDirective(ctx, d, "category", id, "cloud_category_updated", map[string]any{"name": name, "color": color})
	return "updated category " + name, nil
}

// cloudUpdateItemDetails is the update_item_details hook (ut-docs#2324,
// ADR-0095 Decision 1): a PARTIAL update of sku/description/unit/colour/
// is_weighed/stock_untracked — read-modify-write via
// CatalogRepo.UpdateItemPartial (a single BEGIN IMMEDIATE transaction,
// ut-docs#2324 review finding S1) rather than two separate GetItem/
// UpdateItem calls, so a genuinely concurrent local edit to any OTHER
// column (price, name, active state, category/brand/tax) can't be silently
// reverted by a stale read here — same race UpdateItemReturningWasActive's
// own doc comment describes for the local admin editor's own update path.
// sku/description/unit/color are nil when absent from the directive's
// payload; is_weighed/stock_untracked are nil the same way — nil means
// "leave untouched", never "unset" or "false".
//
// A blank sku or unit is normalized to "absent" here (ut-docs#2324 review
// finding S3): updateItemExec's own SQL makes a blank sku a true no-op
// (COALESCE(NULLIF(?,”), sku)) but silently DEFAULTS a blank unit to
// "each" — neither is a meaningful "clear this field" the way blank IS for
// colour (see below), so reporting/auditing either as a real change would
// be false attribution. This normalization runs before the "any fields
// left?" check, so a request carrying only a blank sku/unit correctly
// reports "no changes" rather than a change that didn't actually happen.
//
// Colour is different: "" is a real, valid "no colour" state (same as
// cloudUpsertCategory), so a present-but-empty colour DOES proceed as a
// real, audited change — validated against the SAME fixed palette
// cloudUpsertCategory checks (catalogtypes.ValidItemColor) before anything
// else runs, so a refused colour never reaches the primary gate or the
// write.
//
// CategoryID/BrandID/TaxCodeID are deliberately never touched here, even
// though they're part of catalogtypes.ItemInput: the merchant portal has no
// safe way to offer that picker yet (needs the read-side lookup sync ADR-
// 0095 Decision 2 hasn't shipped, ut-docs#2354) — UpdateItemPartial only
// ever applies the six optional overrides above onto whatever it reads
// inside its own transaction.
//
// items is an admin-synced table (primary-wins pull, sync_admin_repo.go's
// adminTables), so this is gated and audited the same way every other
// catalog-mutating directive is (requirePrimaryDirective/
// auditCloudDirective, ut-docs#2353) — a write accepted here on a replica
// would silently vanish on the next admin pull. The gate runs after colour
// validation (a malformed request should report that specific problem) but
// before the existence check, which now happens inside UpdateItemPartial's
// own transaction — a replica correctly refuses regardless of whether the
// item exists.
func cloudUpdateItemDetails(ctx context.Context, d *common.Deps, itemID string, sku, description, unit, color *string, isWeighed, stockUntracked *bool) (string, error) {
	if sku != nil && strings.TrimSpace(*sku) == "" {
		sku = nil
	}
	if unit != nil && strings.TrimSpace(*unit) == "" {
		unit = nil
	}
	if color != nil {
		c := strings.TrimSpace(*color)
		if !catalogtypes.ValidItemColor(c) {
			return "", fmt.Errorf("colour %q is not one of the item palette colours", c)
		}
		color = &c
	}

	var changed []string
	auditPayload := map[string]any{}
	if sku != nil {
		changed = append(changed, "sku")
		auditPayload["sku"] = *sku
	}
	if description != nil {
		changed = append(changed, "description")
		auditPayload["description"] = *description
	}
	if unit != nil {
		changed = append(changed, "unit")
		auditPayload["unit"] = *unit
	}
	if color != nil {
		changed = append(changed, "color")
		auditPayload["color"] = *color
	}
	if isWeighed != nil {
		changed = append(changed, "is_weighed")
		auditPayload["is_weighed"] = *isWeighed
	}
	if stockUntracked != nil {
		changed = append(changed, "stock_untracked")
		auditPayload["stock_untracked"] = *stockUntracked
	}

	if len(changed) == 0 {
		return "no changes", nil
	}

	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	ok, err := data.NewCatalogRepo(d.Db).UpdateItemPartial(ctx, itemID, sku, description, unit, color, isWeighed, stockUntracked)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("item not found")
	}
	auditCloudDirective(ctx, d, "item", itemID, "cloud_item_details_updated", auditPayload)
	return "details updated: " + strings.Join(changed, ", "), nil
}

// cloudUpsertModifierGroup is the upsert_modifier_group hook (ut-docs#2322
// "modifier groups editor" slice of ut-docs#2289, ADR-0095 Decision 1): the
// cloud panel's per-item modifier-group creator — CREATE-ONLY, the same
// scope cut cloudUpsertCategory shipped with (no id in the payload at all;
// the read-side StoreSnapshot extension, ADR-0095 Decision 2, hasn't
// shipped, so the portal has no way to discover an existing group's id to
// edit by, or to offer an "attach an existing group" picker — see
// cloudUpsertCategory's own doc comment for the full reasoning, which
// applies identically here). item_id is OPTIONAL since ADR-0101
// (ut-docs#2399): blank creates a SHOP-WIDE group with no assignment (the
// merchant assigns it to categories/items from /modifiers, or a later
// directive links it); present, it must name an item that exists on THIS
// till — unlike a category id, item ids ARE already known/surfaced to the
// cloud (every catalog snapshot row carries its own id) — and the new
// group is created and then linked to it, two repo calls in that order (a
// link failure after a successful create leaves a valid unassigned group,
// never a half-written row).
//
// item_modifier_groups (and its link/option tables) is an admin-synced
// table (sync_admin_repo.go's adminTables), same as items/categories, so
// this is gated and audited the same way every other catalog-mutating
// directive is (requirePrimaryDirective/auditCloudDirective, ut-docs#2353).
//
// Directives are at-least-once, so a retried CREATE must not duplicate: an
// existing ACTIVE group with the same name (case-insensitive, matching
// cloudUpsertCategory's own active-only dedupe) — already linked to this
// item when one is given, anywhere in the shop when none is — counts as
// success and is left untouched — the retry is a no-op, not a silent edit
// of a group the merchant may have changed locally since. The idempotency
// scan and the item-existence check both run BEFORE the primary gate, same
// reasoning as cloudCreateItem/cloudUpsertCategory: a replica replaying an
// already-applied create should report success, not a spurious refusal,
// and a read-only existence check is safe on a replica too.
//
// min_select/max_select and each option's price_delta_minor are validated
// independently of the cloud's own check (the cloud already enforces this
// too, but this till does not trust that — "validate all external input"),
// mirroring the till's own CHECK constraints
// (min_select >= 0 AND max_select >= min_select; price_delta_minor >= 0,
// additive-only per data.ModifierRepo.CreateOption's own rule), plus the
// local admin creator's own two normalisations (max_select is clamped to at
// least 1; a "required" group's min_select is raised to at least 1) so a
// cloud-created group is always within the same envelope a locally created
// one is. Options are created in the given order, their sort_order
// following that order — zero options is valid, a group may exist with none
// yet, same as CreateGroup's own rule; if an option insert fails partway,
// the whole group is rolled back rather than left half-created.
func cloudUpsertModifierGroup(ctx context.Context, d *common.Deps, itemID, name string, required bool, minSelect, maxSelect int, options []cloudsync.ModifierGroupOption) (string, error) {
	itemID = strings.TrimSpace(itemID)
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name required")
	}
	if minSelect < 0 || maxSelect < 0 {
		return "", fmt.Errorf("min_select and max_select must be >= 0")
	}
	// The same two normalisations the LOCAL admin creator applies
	// (catalog/handlers.go's POST /api/catalog/modifier-group): a group that
	// can never be picked from is meaningless, and a "required" group that
	// asks for zero picks is a contradiction the sale-time validator cannot
	// enforce — pos_modifiers_api.go checks only MinSelect/MaxSelect, never
	// Required, so required+min_select=0 would render the picker's "*" and
	// still let a pick be skipped server-side. Mirrored here so a
	// cloud-created group can never land in a state the till's own admin UI
	// would refuse to produce (2026-09-17 review, ut-docs#2322).
	if maxSelect < 1 {
		maxSelect = 1
	}
	if required && minSelect < 1 {
		minSelect = 1
	}
	if minSelect > maxSelect {
		return "", fmt.Errorf("min_select must be <= max_select")
	}
	for _, opt := range options {
		if strings.TrimSpace(opt.Name) == "" {
			return "", fmt.Errorf("option name required")
		}
		if opt.PriceDeltaMinor < 0 {
			return "", fmt.Errorf("option %q: price_delta_minor must be >= 0 (additive-only)", opt.Name)
		}
	}

	modRepo := data.NewModifierRepo(d.Db)
	if itemID != "" {
		repo := data.NewCatalogRepo(d.Db)
		if exists, err := repo.ItemExists(ctx, itemID); err != nil {
			return "", err
		} else if !exists {
			return "", fmt.Errorf("item not found")
		}
		existing, err := modRepo.ListAllGroupsForItem(ctx, itemID)
		if err != nil {
			return "", err
		}
		for _, g := range existing {
			// ACTIVE groups only, exactly like cloudUpsertCategory's own
			// dedupe (`c.IsActive && strings.EqualFold(...)`):
			// ListAllGroupsForItem deliberately includes deactivated
			// groups, and treating one of those as "already exists" would
			// refuse a genuinely new create forever while reporting success
			// to the portal — the merchant's request silently dropped
			// (2026-09-17 review, ut-docs#2322).
			if g.IsActive && strings.EqualFold(g.Name, name) {
				return "modifier group " + g.Name + " already exists on this item", nil
			}
		}
	} else {
		// No item: a shop-wide group (ADR-0101). Dedupe against every
		// ACTIVE group in the shop — ListActiveModifierGroups is exactly
		// that set, so a retried standalone create is a no-op too.
		existing, err := modRepo.ListActiveModifierGroups(ctx)
		if err != nil {
			return "", err
		}
		for _, g := range existing {
			if strings.EqualFold(g.Name, name) {
				return "modifier group " + g.Name + " already exists", nil
			}
		}
	}

	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}

	sortOrder := 0
	if itemID != "" {
		next, err := modRepo.NextGroupSortOrderForItem(ctx, itemID)
		if err != nil {
			return "", err
		}
		sortOrder = next
	}
	groupID := uuid.NewString()
	if _, err := modRepo.CreateGroup(ctx, groupID, name, required, minSelect, maxSelect, sortOrder); err != nil {
		return "", err
	}
	// rollBack undoes the whole create on any later failure: directives
	// are at-least-once, and the name dedupe above would otherwise report
	// the retry as "already exists", cementing a half-created group (no
	// link, or missing options) forever while telling the merchant it
	// worked. DeleteGroup cascades the link row and any options already
	// inserted (foreign_keys is ON, db.go), so the retry recreates it
	// cleanly (2026-09-17 review, ut-docs#2322; widened to the link step
	// by ADR-0101, since CreateGroup no longer writes the link itself).
	rollBack := func() {
		if derr := modRepo.DeleteGroup(ctx, groupID); derr != nil {
			log.Printf("[cloudsync] roll back half-created modifier group %s: %v", groupID, derr)
		}
	}
	if itemID != "" {
		if err := modRepo.LinkGroupToItem(ctx, itemID, groupID, sortOrder); err != nil {
			rollBack()
			return "", err
		}
	}
	for i, opt := range options {
		if _, err := modRepo.CreateOption(ctx, uuid.NewString(), groupID, strings.TrimSpace(opt.Name), opt.PriceDeltaMinor, i); err != nil {
			rollBack()
			return "", err
		}
	}
	auditCloudDirective(ctx, d, "modifier_group", groupID, "cloud_modifier_group_created", map[string]any{
		"item_id": itemID, "name": name, "required": required,
		"min_select": minSelect, "max_select": maxSelect, "option_count": len(options),
	})
	return fmt.Sprintf("created modifier group %s (%d option(s))", name, len(options)), nil
}

// cloudCreateItem creates a catalog item from a directive. Directives are
// at-least-once, so a retry must not duplicate: an existing active item with
// the same name counts as success. A barcode already attached elsewhere
// fails the directive (visible in the result column) rather than stealing it.
//
// items is an admin-synced table (primary-wins pull, sync_admin_repo.go's
// adminTables), same as the local admin item-create handler's own
// requirePrimary gate (catalog/handlers.go) — a directive applied on a
// replica till would silently vanish on the next admin pull, so it's
// refused up front instead, same reasoning (ut-docs#2353). The write is
// also audited under the "system" actor, the same pattern cloudAdjustStock
// and sync_admin.go's admin_pulled already use to distinguish a
// cloud-originated mutation from an operator's own action at the till.
func cloudCreateItem(ctx context.Context, d *common.Deps, name string, priceMinor int64, barcode string) (string, error) {
	repo := data.NewCatalogRepo(d.Db)
	// Idempotency check first, gate second: on a replica, the item most
	// directives replay against has usually already arrived via the normal
	// admin pull, so an at-least-once retry that finds the desired state
	// already true should report success, not a spurious replica refusal
	// (ut-docs#2353 review). Only a *real* write attempt needs the gate.
	if _, exists, err := repo.FindActiveItemByName(ctx, name); err == nil && exists {
		return "item already exists", nil
	}
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
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
	auditCloudDirective(ctx, d, "item", id, "cloud_item_created", map[string]any{
		"name": name, "price_minor": priceMinor, "barcode": barcode,
	})
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
