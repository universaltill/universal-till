package plugins

import (
	"fmt"
	"regexp"
	"strings"
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
