package main

import (
	"encoding/json"
	"slices"

	"github.com/universaltill/universal-till/internal/data"
)

// settingsRegistryJSONPath is the committed artefact, relative to the repo root.
const settingsRegistryJSONPath = "docs/till-settings-registry.json"

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

// settingsRegistryJSON renders the registry as the committed artefact:
// {"schema":1,"entries":[...]} in registry order, two-space indent, trailing
// newline. remote_writable is true only for what RemoteWritableKeys allows.
func settingsRegistryJSON() ([]byte, error) {
	remote := data.RemoteWritableKeys()
	rows := make([]registryEntryJSON, 0, len(data.AgentSettings))
	for _, s := range data.AgentSettings {
		r := registryEntryJSON{
			Key: s.Key, Type: s.Type, Tier: s.Tier, Secret: s.Secret, Default: s.Default,
			On: s.On, Off: s.Off, Options: slices.Clone(s.Options),
			LabelKey: s.LabelKey, DescriptionKey: s.HintKey, Region: s.Region,
			MinTillVersion: s.MinTillVersion, Audit: s.Audit,
			WritableByTillAgent: s.Writable, RemoteWritable: remote[s.Key],
		}
		if s.Type == data.SettingTypeInt {
			mn, mx := s.Min, s.Max
			r.Min, r.Max = &mn, &mx
		}
		rows = append(rows, r)
	}
	b, err := json.MarshalIndent(struct {
		Schema               int                 `json:"schema"`
		ExpectMinTillVersion string              `json:"expect_min_till_version"`
		Entries              []registryEntryJSON `json:"entries"`
	}{1, data.RegistryMinTillVersion, rows}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
