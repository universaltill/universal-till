// Package registrydesc derives the descriptor fields of the settings registry
// (scope, restorable, tier gate, visibility class of a region) for the
// registry generator (cmd/gen-settings-registry) and its tests. It is imported
// by no production binary, so the whole-program deadcode baseline never sees
// it (guard-deadcode-baseline.sh); the logic stays next to the data it reads.
package registrydesc

import (
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/uislot"
)

// Descriptor scopes (design-B RC1). The registry is the core half of the unified
// field descriptor; plugin fields add "register" in ut-cloud.
const (
	SettingDescriptorScopeShop   = "shop"
	SettingDescriptorScopeDevice = "device"
)

// DescriptorOwnerCore is the descriptor owner of every registry entry; a
// plugin field's owner is "plugin:<id>" and is built in ut-cloud.
const DescriptorOwnerCore = "core"

// DescriptorScope derives the entry's scope from SettingScope (one
// classification, never a second hand-kept list): "shop" for a shop-wide key,
// "device" for a per-till key, "" for an unclassified one (a test failure).
func DescriptorScope(s data.AgentSetting) string {
	switch data.SettingScope(s.Key) {
	case data.SettingShopWide:
		return SettingDescriptorScopeShop
	case data.SettingPerTill:
		return SettingDescriptorScopeDevice
	}
	return ""
}

// Restorable reports whether my. may re-send the stored value to a new or
// replaced till: the cloud may write it at all (remote-writable, tier normal
// or sensitive, not secret) and no reason keeps it on the till.
func Restorable(s data.AgentSetting) bool {
	return s.RemoteWritable && s.AssistantVisible() && s.NotRestorableReason == ""
}

// Who confirms a change at a tier (ADR-0175 s15.3).
const (
	ConfirmByOwnerOrManager = "owner_or_manager"
	ConfirmByOwner          = "owner"
)

// TierGate is what a change at one tier needs: who confirms it, whether the
// confirm needs a recent step-up (ADR-0141) and which per-shop switch must be
// on. The empty gate means nobody may change it through the cloud.
type TierGate struct {
	ConfirmBy string
	StepUp    bool
	Switch    string
}

// TierGateFor is the gate of a tier. secret, never and an unknown tier have
// none (fail closed).
func TierGateFor(tier string) TierGate {
	switch tier {
	case data.SettingTierNormal:
		return TierGate{ConfirmBy: ConfirmByOwnerOrManager, Switch: "allow_till_settings"}
	case data.SettingTierSensitive:
		return TierGate{ConfirmBy: ConfirmByOwner, StepUp: true, Switch: "allow_sensitive_settings"}
	}
	return TierGate{}
}

// regionCard maps a registry region that is NOT a Settings card of its own (a
// <div>, fieldset or a link into another page) to the card whose class it
// inherits (ADR-0190 amendment 4). A region that is itself a CoreSettings key
// needs no entry here.
var regionCard = map[string]string{
	// A <div> inside the Currency card.
	"settings-currency-display": "settings-currency",
	// The receipt designer is a link on its own page; its keys are shown in
	// the Receipts & printers category, so they follow the printer card.
	"receipt-designer": "settings-printer",
	// The cashier tip form is a fieldset inside the Payments card (ut-docs#7107).
	"settings-payments-tips": "settings-payments",
	// ut-docs#7173: the two kiosk groups are anchors inside the one Kiosk card.
	"settings-kiosk-idle-reset":   "settings-kiosk",
	"settings-kiosk-payment-mode": "settings-kiosk",
}

// CardForRegion returns the Settings card id a registry region belongs to:
// the region itself when it is a card, else the card it is contained in.
func CardForRegion(region string) string {
	if card, ok := regionCard[region]; ok {
		return card
	}
	return region
}

// ClassForRegion returns the base class a registry region carries: the class
// of the CoreSettings row of that id, or of the card it is contained in.
// ok is false when the region names no card (the registry generator fails).
func ClassForRegion(region string) (uislot.Class, bool) {
	key := region
	if card, ok := regionCard[region]; ok {
		key = card
	}
	row, ok := uislot.CoreSettingsEntry(key)
	if !ok || !row.Class.Valid() {
		return "", false
	}
	return row.Class, true
}
