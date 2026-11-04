package data

// The typed till settings registry (ut-docs#5349, ADR-0175 amendment 13).
// It is the AgentSettings catalogue (agent_settings.go) extended with
// everything a remote caller needs to know about a key: its type and bounds,
// its stored spelling, its risk tier and whether the cloud may write it.
// Three formerly hand-kept lists are derived from it: the till's
// set_till_setting whitelist (RemoteWritableKeys), the heartbeat report
// (pages.remoteTillSettingsReport) and the committed artefact
// docs/till-settings-registry.json that ut-cloud consumes
// (cmd/gen-settings-registry; `make gen-settings-registry`). An entry with no valid
// tier fails TestSettingsRegistry_EveryEntryHasATierAndType, so a key cannot
// be added without a risk decision.

// Setting types.
const (
	SettingTypeBool = "bool"
	SettingTypeInt  = "int"
	SettingTypeEnum = "enum"
	SettingTypeText = "text"
	SettingTypeList = "list"
)

// Risk tiers (amendment 13.3).
//
//	normal    owner or manager may change it through the connector.
//	sensitive owner only, with step-up.
//	secret    never readable or writable by an assistant; reports say only
//	          "set" / "not set".
//	never     not a settings key an assistant may touch at all.
const (
	SettingTierNormal    = "normal"
	SettingTierSensitive = "sensitive"
	SettingTierSecret    = "secret"
	SettingTierNever     = "never"
)

// SettingSecretSet and SettingSecretNotSet are what a report says about a
// secret-tier key instead of its value.
const (
	SettingSecretSet    = "set"
	SettingSecretNotSet = "not set"
)

// RegistryMinTillVersion is the first till release whose set_till_setting
// honours `expect` (compare-and-set); an older till ignores the field and
// writes unconditionally, so the cloud must not rely on it below this. The
// next release after v0.35.3; release.yml decides the final number, change
// this constant if it differs.
const RegistryMinTillVersion = "0.36.0"

// ValidSettingTier reports whether t is one of the four tiers.
func ValidSettingTier(t string) bool {
	switch t {
	case SettingTierNormal, SettingTierSensitive, SettingTierSecret, SettingTierNever:
		return true
	}
	return false
}

// RemoteWritableKeysOf is the set_till_setting whitelist drawn from entries:
// remote-writable AND tier normal or sensitive. A secret or never entry, or
// one without a tier, is never in it, whatever its flag says.
func RemoteWritableKeysOf(entries []AgentSetting) map[string]bool {
	out := map[string]bool{}
	for _, s := range entries {
		if s.RemoteWritable && (s.Tier == SettingTierNormal || s.Tier == SettingTierSensitive) {
			out[s.Key] = true
		}
	}
	return out
}

// RemoteWritableKeys is RemoteWritableKeysOf the till's own registry.
func RemoteWritableKeys() map[string]bool { return RemoteWritableKeysOf(AgentSettings) }
