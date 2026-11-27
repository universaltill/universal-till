package shoptemplate

import "testing"

func TestSeedWhitelistIsClosed(t *testing.T) {
	for _, k := range []string{"sale.order_type_prompt", "sale.display_no_scheme", "sale.browsing_mode", "theme"} {
		if !IsSeedable(k) {
			t.Errorf("IsSeedable(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"", "sale.other", "shop.type", "anything"} {
		if IsSeedable(k) {
			t.Errorf("IsSeedable(%q) = true, want false (not on the closed list)", k)
		}
	}
}

func TestSeedDenyPrefixesRefuseMoneyFiscalTaxAuthKeys(t *testing.T) {
	for _, k := range []string{
		"payments.stripe.enabled", "payment.terminal", "fiscal.mode", "tax.rate", "vat.scheme",
		"auth.pin_length", "staff.pin", "security.lock", "cloud.token", "registration.stopped", "privacy.consent", "backup.passphrase",
	} {
		if !IsDeniedSeedKey(k) {
			t.Errorf("IsDeniedSeedKey(%q) = false, want true", k)
		}
		if IsSeedable(k) {
			t.Errorf("IsSeedable(%q) = true, want false", k)
		}
	}
	for _, k := range []string{"sale.order_type_prompt", "theme"} {
		if IsDeniedSeedKey(k) {
			t.Errorf("IsDeniedSeedKey(%q) = true, want false", k)
		}
	}
}

func TestSeedValueValidation(t *testing.T) {
	if !SeedValueValid("sale.order_type_prompt", "off") || SeedValueValid("sale.order_type_prompt", "sideways") {
		t.Error("order_type_prompt must accept off and reject sideways")
	}
	if !SeedValueValid("sale.browsing_mode", "strip_overflow") || SeedValueValid("sale.browsing_mode", "off") {
		t.Error("browsing_mode accepts strip_overflow, not off")
	}
	if !SeedValueValid("theme", "classic") || SeedValueValid("theme", "") {
		t.Error("theme accepts any non-empty name, never empty")
	}
	if SeedValueValid("payments.x", "1") {
		t.Error("off-list key never valid")
	}
}

func TestIsPerTillSeedKeyIsOnlyTheme(t *testing.T) {
	if !IsPerTillSeedKey("theme") || IsPerTillSeedKey("sale.order_type_prompt") {
		t.Error("theme is the single per-till seed (ADR-0190 amendment 1)")
	}
}
