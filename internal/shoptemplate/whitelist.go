package shoptemplate

import "strings"

// The seed whitelist is a closed, literal list (ADR-0190 decision 3): a
// template may pre-set only these shop-wide defaults, once, when the shop
// type is first chosen. Adding a key is a code change plus a review, never a
// template edit. S6 (Apply) shares this list.
//
// seedValues maps each seedable key to its allowed values; a nil set means
// "any non-empty value" (used only by the per-till `theme`, whose names are
// owned by the theme registry and validated by the caller).
var seedValues = map[string]map[string]bool{
	"sale.order_type_prompt": {"top": true, "before_item": true, "at_pay": true, "off": true},
	"sale.display_no_scheme": {"trading_period_reset": true, "lifetime_no_reset": true},
	"sale.browsing_mode":     {"category_tabs": true, "all_filter_chips": true, "strip_overflow": true},
	"theme":                  nil,
}

// perTillSeedKeys are seeded per till, not shop-wide (ADR-0190 amendment 1:
// `theme` is the single exception), and are exempt from the registry `normal`
// tier check because they are not registry rows.
var perTillSeedKeys = map[string]bool{"theme": true}

// seedDenyPrefixes are the families a template can never touch, however the
// whitelist is edited later: money, fiscal, tax, identity and security
// settings. Defence in depth: IsSeedable checks this before the whitelist.
var seedDenyPrefixes = []string{
	"payment.", "payments.", "fiscal.", "tax.", "vat.", "auth.", "staff.", "pin.",
	"permission.", "security.", "cloud.", "registration.", "privacy.", "backup.",
}

// IsDeniedSeedKey reports whether key is in a family no template may seed.
func IsDeniedSeedKey(key string) bool {
	for _, p := range seedDenyPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// IsSeedable reports whether key is on the closed seed whitelist and not in a
// denied family.
func IsSeedable(key string) bool {
	if IsDeniedSeedKey(key) {
		return false
	}
	_, ok := seedValues[key]
	return ok
}

// IsPerTillSeedKey reports whether a seed for key is stored per till rather
// than shop-wide (only `theme`).
func IsPerTillSeedKey(key string) bool { return perTillSeedKeys[key] }

// SeedValueValid reports whether value is allowed for the seedable key.
func SeedValueValid(key, value string) bool {
	if !IsSeedable(key) {
		return false
	}
	allowed := seedValues[key]
	if allowed == nil {
		return value != ""
	}
	return allowed[value]
}
