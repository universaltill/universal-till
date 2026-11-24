package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/bundled"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/updates"
)

// ADR-0172 (amends ADR-0025 Decision 4 narrowly): a new till installs its
// shop country's tax-rate plugin without a click, but only ONE kind of
// plugin: free, first-party, non-fiscal, no network, and naming the country
// explicitly in its `markets`. Everything else stays prompted (ADR-0129 §4).
// This file is the eligibility rule (§2): the catalogue-side filter
// (taxListingEligible, shared by the wizard tile and the installer) and the check on the VERIFIED manifest
// (taxAutoInstallEligible). Neither names a country or a plugin: the
// choice comes from what the plugin declares (core-neutral guard).

// taxAutoInstallIDPrefix is the reserved first-party id prefix. Only
// first-party CI can upload an id under it (ut-docs#2678), and the
// marketplace signature covers the manifest `id` (ADR-0158), so the id on
// the verified manifest is what the till trusts, not the catalogue's word.
const taxAutoInstallIDPrefix = "com.universaltill."

// catalogTrustOfficial is the catalogue trust level of a first-party listing
// (ut-cloud catalog.CatalogTrustLevel).
const catalogTrustOfficial = "official"

// taxAutoInstallPermissions is the allow-list of manifest permissions an
// auto-installed plugin may request (ADR-0172 §2.4, §6): the operator never
// saw the permission badge, so anything else (any `net:` host, any
// `*_register_*` scope, any permission added later) makes it ineligible.
var taxAutoInstallPermissions = []string{"events:receive", "sales:read", "storage"}

// fiscalNamespace is the event/capability namespace of the fiscal extension
// points; a plugin touching it is never auto-installed (ADR-0172 §2.4).
const fiscalNamespace = "fiscal."

// taxAutoInstallEligible checks a VERIFIED manifest (after Ed25519
// verification, before anything is persisted or activated) against
// ADR-0172 §2.2 and §2.4 for the shop's country. nil = eligible; the error
// is the reason, for the log. The listing alone is never trusted for this.
func taxAutoInstallEligible(m *plugins.Manifest, country string) error {
	if m == nil {
		return fmt.Errorf("no manifest")
	}
	if !strings.HasPrefix(m.ID, taxAutoInstallIDPrefix) {
		return fmt.Errorf("id %q is not first-party (%s…)", m.ID, taxAutoInstallIDPrefix)
	}
	if m.CanonicalType != "tax" {
		return fmt.Errorf("canonical_type %q is not tax", m.CanonicalType)
	}
	if len(m.Markets) == 0 {
		return fmt.Errorf("markets is empty (every market): never auto-installed")
	}
	if !slices.ContainsFunc(m.Markets, func(c string) bool { return strings.EqualFold(c, country) }) {
		return fmt.Errorf("markets %v do not name %s", m.Markets, strings.ToUpper(country))
	}
	for _, h := range m.Hooks {
		if isFiscalName(h.Event) {
			return fmt.Errorf("subscribes %s: a fiscal plugin is never auto-installed", h.Event)
		}
	}
	for _, e := range m.Entries {
		if isFiscalName(e.TriggerEvent) {
			return fmt.Errorf("entry %q subscribes %s: a fiscal plugin is never auto-installed", e.Key, e.TriggerEvent)
		}
	}
	for _, p := range m.Provides {
		if isFiscalName(p) {
			return fmt.Errorf("provides %s: a fiscal plugin is never auto-installed", p)
		}
	}
	for _, p := range m.Permissions {
		if !slices.Contains(taxAutoInstallPermissions, p) {
			return fmt.Errorf("permission %q is outside the auto-install allow-list %v", p, taxAutoInstallPermissions)
		}
	}
	return nil
}

func isFiscalName(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), fiscalNamespace)
}

// taxListingEligible applies ADR-0172 §2.1-§2.3 to a catalogue listing for
// country: markets names the country explicitly, first-party (`official`)
// and free, compatible architecture. It also applies the permission
// allow-list the catalogue sends, so a plugin that asks for `net:` or a
// register scope is never even downloaded; the authoritative check is still
// taxAutoInstallEligible on the verified manifest. "" = eligible, otherwise
// the reason.
func taxListingEligible(p *marketplace.PluginSummary, country string) string {
	if !slices.ContainsFunc(p.Markets, func(c string) bool { return strings.EqualFold(c, country) }) {
		return "markets does not name the country explicitly"
	}
	if !strings.EqualFold(strings.TrimSpace(p.TrustTier), catalogTrustOfficial) {
		return "not first-party (trust level " + p.TrustTier + ")"
	}
	if p.PaidListing {
		return "paid listing"
	}
	if !listingArchOK(p.Architectures) {
		return "incompatible architecture"
	}
	for _, perm := range p.Permissions {
		if !slices.Contains(taxAutoInstallPermissions, perm) {
			return "permission " + perm + " is outside the auto-install allow-list"
		}
	}
	return ""
}

// listingArchOK: no architectures listed, "any", or this till's own.
// (Minimum host version is already dropped by marketplace.ListPlugins.)
func listingArchOK(archs []string) bool {
	if len(archs) == 0 {
		return true
	}
	for _, a := range archs {
		switch strings.ToLower(strings.TrimSpace(a)) {
		case "any", runtime.GOOS + "/" + runtime.GOARCH, runtime.GOARCH:
			return true
		}
	}
	return false
}

// countryTaxListings returns the tax listings the catalogue serves for
// country (canonical_type=tax, markets=<country>), eligible or not. A
// listing with no markets is included (the catalogue treats it as every
// market). An unreachable catalogue is an error wrapping
// errBasePluginCatalogUnreachable, never an empty answer.
func countryTaxListings(ctx context.Context, d *common.Deps, country string) ([]marketplace.PluginSummary, error) {
	// enroll.Effective, NOT EnsureRegistered: browsing the catalogue must
	// never mint the shop's cloud identity (ADR-0015), same as the base
	// plugin lookup.
	effCfg := enroll.Effective(d.Cfg)
	client := marketplace.NewClient(&effCfg.Marketplace)

	var all []marketplace.PluginSummary
	pageToken := ""
	for page := 0; page < setupBasePluginMaxPages; page++ {
		resp, err := client.ListPlugins(ctx, &marketplace.ListPluginsRequest{
			Capability: []string{"tax"},
			Market:     country,
			DeviceArch: runtime.GOOS + "/" + runtime.GOARCH,
			PageToken:  pageToken,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errBasePluginCatalogUnreachable, err)
		}
		all = append(all, resp.Plugins...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	out := all[:0]
	for _, p := range all {
		if p.CanonicalType != "tax" {
			continue
		}
		// The server filters by market, but never trust it alone.
		if len(p.Markets) > 0 && !slices.ContainsFunc(p.Markets, func(c string) bool { return strings.EqualFold(c, country) }) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// resolveAndInstallTaxForMarket is resolveAndInstallBasePlugin's branch for a
// tax spec that names a Market (ADR-0172 §3). The install is the normal
// marketplace install (cloudInstallPluginVersionChecked): same download, same
// signature check, same persist. There is no second path and no trust
// shortcut.
//
//   - A Prompted spec (the operator's own tile consent, ADR-0129 §4) takes the
//     highest-semver tax listing for the market and applies no §2 rule.
//   - An automatic spec needs exactly one §2-eligible listing; the verified
//     manifest is re-checked (§2.4) and the module dry-asked (§3) before
//     anything is persisted. Not exactly one eligible listing (a signing
//     plugin, two plugins, a paid or third-party one) installs nothing: the
//     spec is done (nil), and the prompted tile is the fallback.
//
// No listing at all: an automatic spec is done (nothing to install, no chip);
// a Prompted one stays pending (errBasePluginNotPublished, ut-docs#3210).
func resolveAndInstallTaxForMarket(ctx context.Context, d *common.Deps, spec basePluginSpec) (err error) {
	country := strings.ToUpper(strings.TrimSpace(spec.Market))
	// App Store build (ADR-0194): no code is ever fetched, so the marketplace
	// path below cannot serve any market. The bundled UK tax plugin is
	// enabled for a UK shop; every other market is done at once, silently.
	if !buildinfo.CurrentChannel().CodePluginInstallAllowed() {
		return resolveBundledTaxForMarket(ctx, d, spec, country)
	}
	auto := !spec.Prompted
	if auto {
		// An operator who dismissed or uninstalled it keeps it declined, whatever
		// was recorded (a plugin id, or only the market when no id was known
		// yet) and whenever a stale spec or an in-flight tick still holds it
		// (ADR-0172 §5). Done: the caller drops the spec, no chip.
		if taxAutoInstallDeclined(ctx, d) != "" {
			return nil
		}
		// The chip (ADR-0172 §5) follows every attempt of the automatic path.
		defer func() {
			if err != nil {
				recordTaxAutoInstallFailure(ctx, d, spec, err)
			}
		}()
	}
	all, err := countryTaxListings(ctx, d, country)
	if err != nil {
		return err
	}
	if len(all) == 0 || (spec.Prompted && !slices.ContainsFunc(all, func(p marketplace.PluginSummary) bool { return len(p.Markets) > 0 })) {
		if spec.Prompted {
			return errBasePluginNotPublished // the operator consented: keep waiting for it (ut-docs#3210)
		}
		// Nothing published for this country: done, silently. A reachable
		// catalogue with no tax listing is the common case (most countries),
		// never a reason for a perpetual retry or a warning chip.
		recordTaxAutoInstallDone(ctx, d, spec, "", "")
		return nil
	}
	var best *marketplace.PluginSummary
	var pre plugins.PreInstallCheck
	if spec.Prompted {
		for i := range all {
			// The tile only ever offers a listing that NAMES the country
			// (taxTileFor): a listing with no markets (every market) is never
			// what the operator consented to (ADR-0172 §2.1, ADR-0129 §4).
			if len(all[i].Markets) == 0 {
				continue
			}
			if best == nil || updates.Newer(all[i].Version, best.Version) {
				best = &all[i]
			}
		}
	} else {
		var eligible []*marketplace.PluginSummary
		for i := range all {
			if why := taxListingEligible(&all[i], country); why != "" {
				logging.L().Infof("tax auto-install: listing %s not eligible for %s: %s", all[i].ID, country, why)
				continue
			}
			eligible = append(eligible, &all[i])
		}
		if len(eligible) != 1 {
			logging.L().Infof("tax auto-install: %d tax listing(s) for %s, %d eligible: installing nothing, the prompted tile is the fallback",
				len(all), country, len(eligible))
			recordTaxAutoInstallDone(ctx, d, spec, "", "") // no chip: nothing is being installed
			return nil
		}
		best = eligible[0]
		pre = taxAutoInstallPreInstall(d, country)
	}

	listingID := best.ListingID
	if listingID == "" {
		listingID = best.ID
	}
	// Idempotent: this exact listing is already installed and active.
	if status, had, serr := plugins.NewInstallStatusStore(d.Db).Get(ctx, listingID); serr == nil && had && status.State == plugins.InstallStateActive && status.PluginID != "" {
		if auto {
			recordTaxAutoInstallDone(ctx, d, spec, listingID, best.Name)
		}
		return nil
	}
	if auto {
		recordTaxAutoInstallInstalling(ctx, d, spec, best.Name)
	}
	if _, err := cloudInstallPluginVersionChecked(ctx, d, listingID, best.Version, pre, false, nil); err != nil {
		return fmt.Errorf("install %s@%s: %w", listingID, best.Version, err)
	}
	if auto {
		recordTaxAutoInstallDone(ctx, d, spec, listingID, best.Name)
	}
	return nil
}

// taxAutoInstallPreInstall is the gate the automatic tax install adds to the
// normal install path: ADR-0172 §2.4 on the verified manifest, then §3's
// first-load check. Either refusal wraps errBasePluginRefused and nothing is
// persisted or activated.
func taxAutoInstallPreInstall(d *common.Deps, country string) plugins.PreInstallCheck {
	return func(ctx context.Context, m *plugins.Manifest, dir string) error {
		// An operator who uninstalled it keeps it uninstalled (§5).
		if declined := taxAutoInstallDeclined(ctx, d); declined != "" && declined == m.ID {
			return fmt.Errorf("%w: %s was declined by the operator", errBasePluginRefused, m.ID)
		}
		if err := taxAutoInstallEligible(m, country); err != nil {
			logging.L().Warnf("tax auto-install: refused after download: %v", err)
			return fmt.Errorf("%w: %w", errBasePluginRefused, err)
		}
		if err := taxFirstLoadCheck(ctx, d, m, dir); err != nil {
			logging.L().Warnf("tax auto-install: first-load check failed for %s@%s: %v", m.ID, m.Version, err)
			return fmt.Errorf("%w: first-load check: %w", errBasePluginRefused, err)
		}
		return nil
	}
}

// taxFirstLoadCheck is ADR-0172 §3's "first-load check, before activation":
// the verified module is loaded in an unregistered sandbox and sent one dry
// tax.rate.ask (a synthetic line, no sale). It passes when the module loads
// and returns a well-formed answer or a decline. With a plugin's default
// settings a decline is the expected answer, so this proves "loads and
// answers", not "rates are right".
func taxFirstLoadCheck(ctx context.Context, d *common.Deps, m *plugins.Manifest, dir string) error {
	if m.Runtime != "wasm" {
		return fmt.Errorf("runtime %q cannot be sandboxed (want wasm)", m.Runtime)
	}
	exe := m.Executable
	if exe == "" {
		exe = strings.TrimPrefix(m.Entrypoint, "./")
	}
	if exe == "" {
		return fmt.Errorf("no module named in the manifest")
	}
	raw, err := plugins.DryAskWasm(ctx, d.Db, m.ID, m.Version, filepath.Join(dir, exe), taxRateAskEvent, taxRateAskPayload{
		ItemID: "first-load-check", TaxCodeID: "first-load-check", TaxRateBP: 2000,
	})
	if err != nil {
		return err
	}
	return validTaxAskAnswer(raw)
}

// taxAskMaxRateBP is the largest rate (100.00 %) a first-load answer may name.
const taxAskMaxRateBP = 10000

// validTaxAskAnswer: empty is a decline; otherwise a JSON object whose
// rate_bp, when present, is an integer in [0, 100 %]. It decodes into
// taxRateAskResponse, the very type core's reader uses, so the value checked
// is the value core reads: encoding/json matches keys case-insensitively and
// the last duplicate wins, so {"RATE_BP":99999} cannot slip past a check
// that looked up the exact key. null and a missing/null rate_bp decode to 0
// ("no opinion"). (Core treats rate <= 0 as "no opinion"; a negative or
// absurd rate from a plugin that has never answered a real line is a broken
// module, not a decline.)
func validTaxAskAnswer(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var parsed taxRateAskResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("answer is not a JSON object with an integer rate_bp: %w", err)
	}
	if parsed.RateBP < 0 || parsed.RateBP > taxAskMaxRateBP {
		return fmt.Errorf("rate_bp %d is outside 0..%d", parsed.RateBP, taxAskMaxRateBP)
	}
	return nil
}

// bundledTaxForShop is the bundled tax plugin the App Store build enables for
// a shop in country (the lock's enable_for_countries; ut-docs#6626). Only the
// appstore channel with the embedded set takes this path.
func bundledTaxForShop(country string) (string, bool) {
	if buildinfo.CurrentChannel().CodePluginInstallAllowed() || !bundled.Available() {
		return "", false
	}
	return bundled.EnabledByDefaultFor(country)
}

// resolveBundledTaxForMarket is resolveAndInstallTaxForMarket on the App
// Store build. VAT is a legal necessity, not a payment switch, so the
// bundled plugin the lock names for the shop's country is switched ON by this
// setup path (the boot sync installs it disabled, with no country in sight);
// a shop that declined it keeps it off (ADR-0172 §5). Other countries have no
// bundled tax plugin and no marketplace to fetch one from: done, no chip, no
// network. A bundled plugin not installed yet (boot sync failed) stays
// pending and is retried.
func resolveBundledTaxForMarket(ctx context.Context, d *common.Deps, spec basePluginSpec, country string) error {
	id, ok := bundledTaxForShop(country)
	if !ok {
		saveTaxAutoInstallStatus(ctx, d, nil)
		return nil
	}
	if declined := taxAutoInstallDeclined(ctx, d); declined != "" {
		return nil
	}
	repo := data.NewPluginRepo(d.Db)
	trust, found, err := repo.PluginTrustLevel(ctx, id)
	if err != nil {
		return err
	}
	if !found || trust != "system" {
		return fmt.Errorf("bundled tax plugin %s is not installed yet", id)
	}
	if on, err := repo.PluginActive(ctx, id); err != nil {
		return err
	} else if !on {
		if err := repo.SetPluginActive(ctx, nil, id, true); err != nil {
			return fmt.Errorf("enable bundled tax plugin %s: %w", id, err)
		}
		if err := d.ReloadPlugins(ctx); err != nil {
			logging.L().Warnf("tax: reload after enabling the bundled %s: %v", id, err)
		}
	}
	if !spec.Prompted {
		saveTaxAutoInstallStatus(ctx, d, nil) // no marketplace install happened: nothing to chip
	}
	return nil
}
