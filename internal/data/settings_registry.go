package data

import (
	"encoding/json"
	"slices"
)

// The typed till settings registry (ut-docs#5349, ADR-0175 amendment 13).
// It is the AgentSettings catalogue (agent_settings.go) extended with
// everything a remote caller needs to know about a key: its type and bounds,
// its stored spelling, its risk tier and whether the cloud may write it.
// Three formerly hand-kept lists are derived from it: the till's
// set_till_setting whitelist (RemoteWritableKeys), the heartbeat report
// (pages.remoteTillSettingsReport) and the committed artefact
// docs/till-settings-registry.json that ut-cloud consumes
// (SettingsRegistryJSON; `make gen-settings-registry`). An entry with no valid
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

// SettingsRegistryJSONPath is the committed artefact, relative to the repo
// root.
const SettingsRegistryJSONPath = "docs/till-settings-registry.json"

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

// registryEntryJSON is one artefact row. No values, ever: the artefact is
// the schema of the till's settings, not a shop's data.
type registryEntryJSON struct {
	Key                 string   `json:"key"`
	Type                string   `json:"type"`
	Tier                string   `json:"tier"`
	Secret              bool     `json:"secret"`
	Default             string   `json:"default"`
	On                  string   `json:"on"`
	Off                 string   `json:"off"`
	Min                 *int     `json:"min"`
	Max                 *int     `json:"max"`
	Options             []string `json:"options"`
	LabelKey            string   `json:"label_key"`
	DescriptionKey      string   `json:"description_key"`
	Region              string   `json:"region"`
	MinTillVersion      string   `json:"min_till_version"`
	Audit               string   `json:"audit"`
	WritableByTillAgent bool     `json:"writable_by_till_agent"`
	RemoteWritable      bool     `json:"remote_writable"`
}

// SettingsRegistryJSON renders the registry as the committed artefact:
// {"schema":1,"entries":[...]} in registry order, two-space indent, trailing
// newline. remote_writable is true only for what RemoteWritableKeys allows.
func SettingsRegistryJSON() ([]byte, error) {
	remote := RemoteWritableKeys()
	rows := make([]registryEntryJSON, 0, len(AgentSettings))
	for _, s := range AgentSettings {
		r := registryEntryJSON{
			Key: s.Key, Type: s.Type, Tier: s.Tier, Secret: s.Secret, Default: s.Default,
			On: s.On, Off: s.Off, Options: slices.Clone(s.Options),
			LabelKey: s.LabelKey, DescriptionKey: s.HintKey, Region: s.Region,
			MinTillVersion: s.MinTillVersion, Audit: s.Audit,
			WritableByTillAgent: s.Writable, RemoteWritable: remote[s.Key],
		}
		if s.Type == SettingTypeInt {
			mn, mx := s.Min, s.Max
			r.Min, r.Max = &mn, &mx
		}
		rows = append(rows, r)
	}
	b, err := json.MarshalIndent(struct {
		Schema               int                 `json:"schema"`
		ExpectMinTillVersion string              `json:"expect_min_till_version"`
		Entries              []registryEntryJSON `json:"entries"`
	}{1, RegistryMinTillVersion, rows}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
