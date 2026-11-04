package data

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync/atomic"
)

// The AI-agent settings catalogue (ADR-0153 Decision 3 and Decision 1,
// ut-docs#4129). One entry per setting the Settings screen shows as a
// shop-wide region: how the assistant describes it (the screen's own locale
// keys, so the words match what the operator sees), where it lives
// (a deep link to the card) and, only for an explicit short list, whether
// the assistant may change it through settings.set.v1.
//
// Fail closed in both directions:
//   - a key is writable only if its entry says so AND names the audit
//     action the manual screen writes; everything else (fiscal, tax,
//     payment providers, secrets, users, PINs, roles, plugin install,
//     update channel, cloud pairing, AI settings) is never writable;
//   - a value is readable only if the entry says so (ReadValue); a secret
//     or personal-data setting never is.
//
// The pages package test TestAgentSettingsCatalogue_CoversEverySettingsRegion
// fails when a Settings region has no entry, so coverage cannot drift.
// The catalogue is also the typed registry of ut-docs#5349: see
// settings_registry.go.

// AgentSetting kinds.
const (
	AgentSettingBool = "bool"
	AgentSettingText = "text"
)

// AgentSetting is one catalogue entry.
type AgentSetting struct {
	// Key is the settings-table key.
	Key string
	// Region is the Settings card id (web/ui/pages/settings.html), the
	// anchor of the deep link /settings#<region>.
	Region string
	// LabelKey and HintKey are core locale keys the Settings screen shows.
	// HintKey may be empty when the screen has no help line.
	LabelKey, HintKey string
	Kind              string
	// ReadValue: the current value may be shown to the assistant.
	ReadValue bool
	// Writable: settings.set.v1 may change it (bool kind only for now).
	// Needs Audit, On and Off.
	Writable bool
	// Audit is the audit action the manual screen writes for this key.
	Audit string
	// On and Off are how a bool is stored. Default is the stored spelling
	// the key reads as when unset.
	On, Off, Default string

	// Registry fields (ut-docs#5349, settings_registry.go).
	//
	// Type is bool|int|enum|text|list; Kind stays the two-valued view the
	// assistant's describe tool uses.
	Type string
	// Tier is the risk tier (normal|sensitive|secret|never); every entry
	// must have one.
	Tier string
	// Secret marks a value that must never leave the till: reports say only
	// set / not set. A secret entry has tier secret.
	Secret bool
	// Min, Max (inclusive) and Unit bound an int; Options is the closed
	// value set of an enum (empty: the set is dynamic, such as installed
	// languages, and validated by a special rule).
	Min, Max int
	Unit     string
	Options  []string
	// MinTillVersion is the first till release that carries the entry; ""
	// means it predates the registry (every slice-1 entry: the keys have
	// been settable since ut-docs#2289/#3390). Only a NEW entry sets it.
	MinTillVersion string
	// RemoteWritable: the cloud's set_till_setting directive may change it
	// (tier normal or sensitive only, see RemoteWritableKeysOf). Independent
	// of Writable, which is the on-till agent's switch (ADR-0153 Decision 3).
	RemoteWritable bool
	// Page is the deep link when the setting is not on /settings (Region is
	// then only a label).
	Page string
}

// AgentSettings is the catalogue, in Settings-screen order. It is also the
// typed registry (settings_registry.go): add a key here, with a tier, or it
// does not exist for any remote caller.
var AgentSettings = []AgentSetting{
	{Key: KeyAllowNegativeInventory, Region: "settings-stock-tracking",
		LabelKey: "settings.stock_tracking.label", HintKey: "settings.stock_tracking.hint",
		Kind: AgentSettingBool, ReadValue: true, Writable: true,
		Audit: "allow_negative_inventory_changed", On: "true", Off: "false", Default: "false",
		Type: SettingTypeBool, Tier: SettingTierNormal, RemoteWritable: true},
	{Key: CatalogImportBarcodeFromSKUDefaultKey, Region: "settings-catalog-import-barcode-default",
		LabelKey: "settings.catalog_import_barcode_default.label", HintKey: "settings.catalog_import_barcode_default.hint",
		Kind: AgentSettingBool, ReadValue: true, Writable: true,
		Audit: "catalog_import_barcode_default_changed", On: "1", Off: "0", Default: "0",
		Type: SettingTypeBool, Tier: SettingTierNormal, RemoteWritable: true},
	{Key: CatalogPrePackUnitPriceEnabledKey, Region: "settings-catalog-pre-pack-unit-price",
		LabelKey: "settings.catalog_pre_pack_unit_price.label", HintKey: "settings.catalog_pre_pack_unit_price.hint",
		Kind: AgentSettingBool, ReadValue: true, Writable: true,
		Audit: "catalog_pre_pack_unit_price_changed", On: "1", Off: "0", Default: "0",
		Type: SettingTypeBool, Tier: SettingTierNormal, RemoteWritable: true},

	// Not writable by the on-till agent from here on: the assistant
	// describes them and links to the card. RemoteWritable ones are the
	// cloud's set_till_setting set (ut-docs#2289, #3390); the rest are
	// read-only to every remote caller until a later slice gives them a
	// validation rule.
	{Key: "marketplace.auto_register_opt_in", Region: "registration",
		LabelKey: "settings.enrol.auto_register.label", HintKey: "settings.enrol.auto_register.help",
		Kind: AgentSettingBool, ReadValue: true, On: "true", Off: "false",
		Type: SettingTypeBool, Tier: SettingTierSensitive}, // cloud registration: a destination decision
	{Key: "update.auto_enabled", Region: "settings-update",
		LabelKey: "settings.update.auto_enable", Kind: AgentSettingBool, On: "true", Off: "false", // no value: unset means ON on a main till, which only the screen's own reader knows
		Type: SettingTypeBool, Tier: SettingTierSensitive}, // changes what code the till runs
	{Key: "payments.default_method", Region: "settings-payments",
		LabelKey: "settings.payments.title", HintKey: "settings.payments.default_help", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeText, Tier: SettingTierSensitive},
	{Key: SaleDisplayNoSchemeKey, Region: "settings-order-no",
		LabelKey: "settings.order_no.title", HintKey: "settings.order_no.help", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeEnum, Tier: SettingTierNormal, RemoteWritable: true,
		Options: []string{DisplayNoSchemeTradingPeriodReset, DisplayNoSchemeLifetimeNoReset}},
	{Key: OrderTypePromptModeKey, Region: "settings-order-type-prompt",
		LabelKey: "settings.order_type_prompt.title", HintKey: "settings.order_type_prompt.help", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeEnum, Tier: SettingTierNormal, RemoteWritable: true, Default: OrderTypePromptModeTop,
		Options: []string{OrderTypePromptModeTop, OrderTypePromptModeBeforeItem, OrderTypePromptModeAtPay, OrderTypePromptModeOff}},
	{Key: BarcodeEnabledSymbologiesKey, Region: "settings-barcode",
		LabelKey: "settings.barcode.symbologies_title", HintKey: "settings.barcode.symbologies_help", Kind: AgentSettingText,
		Type: SettingTypeList, Tier: SettingTierNormal, RemoteWritable: true}, // JSON id list, ids from the barcode registry
	{Key: "sale.browsing_mode", Region: "settings-sell-screen",
		LabelKey: "settings.sell_screen.browsing_mode_label", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeEnum, Tier: SettingTierNormal, RemoteWritable: true, Default: "category_tabs",
		Options: []string{"category_tabs", "all_filter_chips", "strip_overflow"}},
	{Key: ReportRetentionModeKey, Region: "settings-retention",
		LabelKey: "settings.retention.title", HintKey: "settings.retention.help", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeEnum, Tier: SettingTierSensitive}, // shortening it deletes records (ut-docs#3390)
	{Key: "invoice.seller_name", Region: "settings-invoice",
		LabelKey: "settings.invoice.title", HintKey: "settings.invoice.help", Kind: AgentSettingText,
		Type: SettingTypeText, Tier: SettingTierSensitive, RemoteWritable: true}, // seller identity
	{Key: "invoice.seller_address", Region: "settings-invoice",
		LabelKey: "settings.invoice.seller_address", HintKey: "settings.invoice.help", Kind: AgentSettingText,
		Type: SettingTypeText, Tier: SettingTierSensitive, RemoteWritable: true}, // seller identity
	{Key: "invoice.seller_vat_no", Region: "settings-invoice",
		LabelKey: "settings.invoice.seller_vat_no", HintKey: "settings.invoice.help", Kind: AgentSettingText,
		Type: SettingTypeText, Tier: SettingTierSensitive, RemoteWritable: true}, // seller identity (tax id)
	{Key: "auth.idle_lock_minutes", Region: "settings-idle-lock",
		LabelKey: "settings.idle_lock.title", HintKey: "settings.idle_lock.help", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeInt, Tier: SettingTierSensitive, RemoteWritable: true, // security: how long an unattended till stays open
		Min: 0, Max: 480, Unit: "minutes", Default: "10"},
	{Key: "kiosk.idle_reset_seconds", Region: "settings-kiosk-idle-reset",
		LabelKey: "settings.kiosk_idle_reset.title", HintKey: "settings.kiosk_idle_reset.help", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeInt, Tier: SettingTierNormal, RemoteWritable: true,
		Min: 0, Max: 600, Unit: "seconds", Default: "60"},
	{Key: "kiosk.payment_mode", Region: "settings-kiosk-payment-mode",
		LabelKey: "settings.kiosk.payment_mode", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeEnum, Tier: SettingTierSensitive, RemoteWritable: true, Default: "kiosk", // payment flow
		Options: []string{"kiosk", "counter"}},
	{Key: "marketplace.telemetry_opt_in", Region: "settings-telemetry",
		LabelKey: "settings.telemetry.enable", Kind: AgentSettingBool, ReadValue: true, On: "true", Off: "false",
		Type: SettingTypeBool, Tier: SettingTierSensitive}, // data leaves the till
	{Key: "store.name", Region: "settings-store-name",
		LabelKey: "settings.store_name.title", HintKey: "settings.store_name.help", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeText, Tier: SettingTierNormal},
	{Key: "store.currency", Region: "settings-currency",
		LabelKey: "settings.currency.title", HintKey: "settings.currency.help", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeText, Tier: SettingTierSensitive}, // changes the meaning of every price (ut-docs#3390)
	{Key: "store.locale", Region: "settings-language",
		LabelKey: "settings.language.title", HintKey: "settings.language.help", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeEnum, Tier: SettingTierNormal, RemoteWritable: true}, // options: the languages installed on the till
	{Key: "store.staff_locales", Region: "settings-staff-languages",
		LabelKey: "settings.staff_languages.title", HintKey: "settings.staff_languages.help", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeList, Tier: SettingTierNormal, RemoteWritable: true},
	{Key: "shop.type", Region: "settings-shop-type",
		LabelKey: "settings.shop_type.title", HintKey: "settings.shop_type.help", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeText, Tier: SettingTierSensitive}, // drives tax/fiscal defaults (ut-docs#3390)

	// Not on the /settings page: receipt text lives on the receipt designer,
	// the receipt policy on the printer card. Receipt header/footer lines
	// are sensitive: they usually carry the seller's name, address and VAT
	// number (owner default, ut-docs#5349).
	{Key: "printer.receipt_policy", Region: "settings-printer",
		LabelKey: "settings.printer.receipt_policy.title", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeEnum, Tier: SettingTierNormal, RemoteWritable: true,
		Options: []string{"always", "ask", "never"}}, // plus the country plugin's allowed subset, checked at write
	{Key: "receipt.header1", Region: "receipt-designer", Page: "/receipt-designer",
		LabelKey: "designer.receipt.header", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeText, Tier: SettingTierSensitive, RemoteWritable: true},
	{Key: "receipt.header2", Region: "receipt-designer", Page: "/receipt-designer",
		LabelKey: "designer.receipt.header", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeText, Tier: SettingTierSensitive, RemoteWritable: true},
	{Key: "receipt.header3", Region: "receipt-designer", Page: "/receipt-designer",
		LabelKey: "designer.receipt.header", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeText, Tier: SettingTierSensitive, RemoteWritable: true},
	{Key: "receipt.footer", Region: "receipt-designer", Page: "/receipt-designer",
		LabelKey: "designer.receipt.footer", Kind: AgentSettingText, ReadValue: true,
		Type: SettingTypeText, Tier: SettingTierSensitive, RemoteWritable: true},
}

// AssistantVisible reports whether an assistant (the on-till agent or the
// connector) may be shown or told about this entry's value at all: tier
// normal or sensitive and not a secret. A secret or never entry, or one
// without a tier, never is (ADR-0175 amendment 13.3).
func (s AgentSetting) AssistantVisible() bool {
	return !s.Secret && (s.Tier == SettingTierNormal || s.Tier == SettingTierSensitive)
}

// LookupAgentSetting returns the catalogue entry for key.
func LookupAgentSetting(key string) (AgentSetting, bool) {
	for _, s := range AgentSettings {
		if s.Key == key {
			return s, true
		}
	}
	return AgentSetting{}, false
}

// BoolState interprets a stored value (found=false: unset) as on/off; an
// unset or unparseable value is the entry's default.
func (s AgentSetting) BoolState(raw string, found bool) bool {
	if found {
		if b, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			return b
		}
	}
	b, _ := strconv.ParseBool(s.Default)
	return b
}

// Stored is the spelling a bool entry is stored as.
func (s AgentSetting) Stored(on bool) string {
	if on {
		return s.On
	}
	return s.Off
}

// rowQuerier is *sql.DB or *sql.Tx.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// AgentSettingRaw reads a key's stored value through db or an open tx.
func AgentSettingRaw(ctx context.Context, q rowQuerier, key string) (string, bool, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return raw, true, nil
}

// AgentSettingViewRow is a row of settings.describe.v1: one catalogue entry
// as the assistant sees it. Value is "on"/"off" for a bool or the stored
// text, and only when the entry is ReadValue; a secret has no entry at all.
type AgentSettingViewRow struct {
	Key      string `json:"key"`
	Link     string `json:"link"`
	Label    string `json:"label"`
	Help     string `json:"help,omitempty"`
	Kind     string `json:"kind"`
	Writable bool   `json:"writable"`
	Value    string `json:"value,omitempty"`
}

// agentSettingsText translates a core locale key for the describe view;
// internal/pages installs the till's translator (data cannot import httpx).
var agentSettingsText atomic.Pointer[func(key string) string]

// SetAgentSettingsText installs (nil: removes) the translator and returns
// the previous one, so a test stub can put it back. With none installed the
// view returns the locale keys themselves.
func SetAgentSettingsText(f func(key string) string) (prev func(key string) string) {
	var p *func(key string) string
	if f != nil {
		p = &f
	}
	if old := agentSettingsText.Swap(p); old != nil {
		prev = *old
	}
	return prev
}

const agentSettingValueMax = 200

func runSettingsDescribe(ctx context.Context, db *sql.DB, _ map[string]int) (any, error) {
	tr := func(k string) string { return k }
	if f := agentSettingsText.Load(); f != nil {
		tr = *f
	}
	rows := make([]AgentSettingViewRow, 0, len(AgentSettings))
	for _, s := range AgentSettings {
		link := "/settings#" + s.Region
		if s.Page != "" {
			link = s.Page
		}
		r := AgentSettingViewRow{Key: s.Key, Link: link, Label: tr(s.LabelKey), Kind: s.Kind, Writable: s.Writable}
		if s.HintKey != "" {
			r.Help = tr(s.HintKey)
		}
		if s.ReadValue && s.AssistantVisible() {
			raw, found, err := AgentSettingRaw(ctx, db, s.Key)
			if err != nil {
				return nil, err
			}
			switch {
			case s.Kind == AgentSettingBool && s.On != "":
				r.Value = "off"
				if s.BoolState(raw, found) {
					r.Value = "on"
				}
			case found:
				r.Value = strings.TrimSpace(raw)
				if len(r.Value) > agentSettingValueMax {
					r.Value = r.Value[:agentSettingValueMax]
				}
			}
		}
		rows = append(rows, r)
	}
	return rows, nil
}
