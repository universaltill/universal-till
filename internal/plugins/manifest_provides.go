package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0129 §2: the closed set of `provides` capabilities. New values need an
// ADR amendment and a core release. ut-cloud's pkg/manifest keeps the same
// set for upload validation.
const (
	// CapabilityFiscalDevice drives a certified fiscal device (the TR ÖKC
	// today). Exclusive and first-party only (ADR-0129 §2).
	CapabilityFiscalDevice = "fiscal.device"
	// CapabilityFiscalRegister owns §146a-style till/signer register rows
	// (ADR-0072's storage namespace). Exclusive and first-party only.
	CapabilityFiscalRegister = "fiscal.register"
	// CapabilityAI is the AI engine (ADR-0126).
	CapabilityAI = "ai"
	// CapabilityLayoutShopTypePrefix + a ShopTypes value names the builtin
	// layout for that shop type. Embedded-builtin only.
	CapabilityLayoutShopTypePrefix = "layout.shop_type:"
)

// shopTypes is the ADR-0026 shop-type taxonomy (café, retail, service trade,
// hospitality, market stall/pop-up, other). The setup wizard and Settings
// offer exactly these; `layout.shop_type:<type>` must name one of them.
// ut-cloud's pkg/manifest mirrors it (TestProvidesClosedSetMirrorsPOS).
var shopTypes = []string{"cafe", "retail", "service", "hospitality", "market_stall", "other"}

// ShopTypes returns a copy of the ADR-0026 shop types, in display order.
func ShopTypes() []string { return append([]string(nil), shopTypes...) }

// IsKnownShopType reports whether v is one of ShopTypes.
func IsKnownShopType(v string) bool {
	for _, t := range shopTypes {
		if v == t {
			return true
		}
	}
	return false
}

// IsKnownCapability reports whether v is in the ADR-0129 closed set.
func IsKnownCapability(v string) bool {
	switch v {
	case CapabilityFiscalDevice, CapabilityFiscalRegister, CapabilityAI:
		return true
	}
	if st, ok := strings.CutPrefix(v, CapabilityLayoutShopTypePrefix); ok {
		return IsKnownShopType(st)
	}
	return false
}

// marketCodeRe is the ISO 3166-1 alpha-2 shape (upper case). Shape only: the
// field says where a plugin is *for*, never gates a legal safeguard
// (ADR-0129 §3), so a well-formed but unassigned code costs nothing.
var marketCodeRe = regexp.MustCompile(`^[A-Z]{2}$`)

func validateProvidesAndMarkets(m *Manifest) error {
	seen := make(map[string]bool, len(m.Provides))
	for _, p := range m.Provides {
		if !IsKnownCapability(p) {
			return fmt.Errorf("manifest provides %q is not a known capability (allowed: %s, %s, %s, %s<%s>)",
				p, CapabilityFiscalDevice, CapabilityFiscalRegister, CapabilityAI,
				CapabilityLayoutShopTypePrefix, strings.Join(shopTypes, "|"))
		}
		if seen[p] {
			return fmt.Errorf("manifest provides %q more than once", p)
		}
		seen[p] = true
	}
	seenMarket := make(map[string]bool, len(m.Markets))
	for _, c := range m.Markets {
		if !marketCodeRe.MatchString(c) {
			return fmt.Errorf("manifest markets %q is not an upper-case ISO 3166-1 alpha-2 country code", c)
		}
		if seenMarket[c] {
			return fmt.Errorf("manifest markets %q more than once", c)
		}
		seenMarket[c] = true
	}
	return nil
}

// fiscalCapabilityPrefix marks the `fiscal.*` capabilities, which are
// exclusive: at most one INSTALLED plugin (active or disabled) may provide
// each (ADR-0129 §2). Today the hard-coded plugin IDs are what stop another
// plugin owning the §146a register namespace or the TR fiscal-device gate;
// `provides` must not reopen that.
const fiscalCapabilityPrefix = "fiscal."

// IsExclusiveCapability reports whether at most one installed plugin may
// provide capability (every `fiscal.*` value).
func IsExclusiveCapability(capability string) bool {
	return strings.HasPrefix(capability, fiscalCapabilityPrefix)
}

// FiscalProvidesConflict reports the first exclusive capability in
// provides that an installed plugin other than pluginID already provides.
// tx is the install/rollback transaction, nil from the enable handler.
// found=false means no conflict (the plugin's own rows never conflict, so
// a self-update or re-enable is fine). A DB error is returned for the
// caller to fail CLOSED on.
func FiscalProvidesConflict(ctx context.Context, repo *data.PluginRepo, tx *sql.Tx, pluginID string, provides []string) (capability, ownerID, ownerName string, found bool, err error) {
	for _, c := range provides {
		if !IsExclusiveCapability(c) {
			continue
		}
		ownerID, ownerName, found, err = repo.CapabilityProviderOwner(ctx, tx, c, pluginID)
		if err != nil || found {
			return c, ownerID, ownerName, found, err
		}
	}
	return "", "", "", false, nil
}

// validateFiscalProvidesExclusivity refuses, inside the install or
// rollback transaction and before anything is written, a manifest that
// would make pluginID a second installed provider of a `fiscal.*`
// capability (ADR-0129 §2; the ADR-0106 preset pattern). A DB error fails
// CLOSED: "couldn't verify" must never let a second §146a register owner
// or fiscal-device provider in.
func validateFiscalProvidesExclusivity(ctx context.Context, repo *data.PluginRepo, tx *sql.Tx, pluginID string, provides []string) error {
	capability, ownerID, ownerName, found, err := FiscalProvidesConflict(ctx, repo, tx, pluginID, provides)
	if err != nil {
		return fmt.Errorf("check fiscal capability exclusivity: %w", err)
	}
	if found {
		return fmt.Errorf("%s (%s) already provides %s — only one installed plugin may provide each fiscal.* capability (ADR-0129); uninstall it before installing %s",
			ownerName, ownerID, capability, pluginID)
	}
	return nil
}

// persistProvidesAndMarkets replaces pluginID's persisted provides and
// markets with the manifest's (PersistManifest and Rollback).
func persistProvidesAndMarkets(ctx context.Context, repo *data.PluginRepo, tx *sql.Tx, pluginID string, m *Manifest) error {
	if err := repo.ReplacePluginProvides(ctx, tx, pluginID, m.Provides); err != nil {
		return fmt.Errorf("persist plugin provides: %w", err)
	}
	if err := repo.ReplacePluginMarkets(ctx, tx, pluginID, m.Markets); err != nil {
		return fmt.Errorf("persist plugin markets: %w", err)
	}
	return nil
}
