// Package pluginsettingsreport builds the till's `plugin_settings` check-in
// report (ADR-0214 Decision 2, ut-docs#6969; contract ut-docs
// reference/contracts/till-plugin-settings.md): for every active plugin, the
// current value of each global-scope setting its installed, signature-
// verified manifest declares, so my. can show them read-only.
//
// It is the one place outside internal/cloudsync that reads plugin settings
// for the cloud (the ADR-0082 guard names it, see
// internal/cloudsync/plugin_settings_boundary_test.go). The rules:
//
//   - a secret value NEVER leaves: a `type: "secret"` setting, a key whose
//     name matches secrets.IsSecretSettingKey, a `report: false` setting and
//     an endpoint URL carrying credentials report only `set` (plus `last4`
//     for a declared secret whose manifest opts in and whose value is 16+
//     characters);
//   - a non-secret value is at most MaxValueRunes characters, a choice must
//     be one of its options, list/map and register-scope settings are not
//     reported (an over-long or invalid value drops the entry);
//   - local data only, never the network; the output is sorted and
//     deterministic so the device-report hash only moves on a real change.
package pluginsettingsreport

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/secrets"
)

const (
	// MaxValueRunes caps a reported non-secret value.
	MaxValueRunes = 256
	// MaxPlugins and MaxSettingsPerPlugin and MaxEntries cap the report
	// (truncate, never fail the check-in); the cloud enforces the same.
	MaxPlugins           = 100
	MaxSettingsPerPlugin = 64
	MaxEntries           = 600
	// secretLast4MinRunes: a value shorter than this never reports last4.
	secretLast4MinRunes = 16
	// InitialVer / OriginTill: until phase 2 adds per-field versioning,
	// every existing value is version 1 with origin "till".
	InitialVer = 1
	OriginTill = "till"
)

// Setting is one reported setting. Value is set for a non-secret value (a
// pointer so a blank "" is told apart from "not reported"); Set/Last4 for a
// withheld one.
type Setting struct {
	Key    string  `json:"key"`
	Ver    int     `json:"ver"`
	Origin string  `json:"origin"`
	At     string  `json:"at,omitempty"`
	Value  *string `json:"value,omitempty"`
	Set    *bool   `json:"set,omitempty"`
	Last4  string  `json:"last4,omitempty"`
}

// PluginEntry is one plugin's settings.
type PluginEntry struct {
	ID       string    `json:"id"`
	Version  string    `json:"version"`
	Settings []Setting `json:"settings"`
}

// Row is a stored value (opened by the repository; Unreadable = a sealed
// value this till cannot open).
type Row struct {
	ValueJSON  string
	Unreadable bool
	UpdatedAt  time.Time
}

// Plugin is Build's input: the installed manifest's settings and the stored
// global-scope rows by key.
type Plugin struct {
	ID       string
	Version  string
	Settings []plugins.ManifestSetting
	Rows     map[string]Row
}

// Build is the pure report builder. Never nil.
func Build(in []Plugin) []PluginEntry {
	sorted := append([]Plugin(nil), in...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	out := make([]PluginEntry, 0, len(sorted))
	total := 0
	for _, p := range sorted {
		if len(out) == MaxPlugins || total >= MaxEntries {
			break
		}
		entry := PluginEntry{ID: p.ID, Version: p.Version, Settings: []Setting{}}
		for _, ms := range p.Settings {
			if len(entry.Settings) == MaxSettingsPerPlugin || total >= MaxEntries {
				break
			}
			if s, ok := buildSetting(ms, p.Rows[ms.Key], hasRow(p.Rows, ms.Key)); ok {
				entry.Settings = append(entry.Settings, s)
				total++
			}
		}
		if len(entry.Settings) > 0 {
			out = append(out, entry)
		}
	}
	return out
}

func hasRow(rows map[string]Row, k string) bool { _, ok := rows[k]; return ok }

func buildSetting(ms plugins.ManifestSetting, row Row, found bool) (Setting, bool) {
	if ms.Scope != "" && ms.Scope != "global" {
		return Setting{}, false // register/user scope: per-till rows come in a later phase
	}
	if ms.Type == plugins.SettingTypeList || ms.Type == plugins.SettingTypeMap {
		return Setting{}, false
	}
	s := Setting{Key: ms.Key, Ver: InitialVer, Origin: OriginTill}
	if found && !row.UpdatedAt.IsZero() {
		s.At = row.UpdatedAt.UTC().Format(time.RFC3339)
	}
	decoded, isSet := decode(row)

	declaredSecret := ms.Type == plugins.SettingTypeSecret
	withhold := declaredSecret || secrets.IsSecretSettingKey(ms.Key) ||
		(ms.Report != nil && !*ms.Report) ||
		(ms.Type == plugins.SettingTypeEndpoint && urlHasCredentials(decoded)) ||
		urlValueCarriesCredentials(decoded) // an untyped or text URL setting is no safer than an endpoint one
	if withhold {
		set := isSet
		s.Set = &set
		if declaredSecret && ms.SecretLast4 && isSet && utf8.RuneCountInString(decoded) >= secretLast4MinRunes {
			r := []rune(decoded)
			s.Last4 = string(r[len(r)-4:])
		}
		return s, true
	}

	if !isSet {
		blank := ""
		s.Value = &blank
		return s, true
	}
	if utf8.RuneCountInString(decoded) > MaxValueRunes || !printable(decoded) {
		return Setting{}, false
	}
	if ms.Type == plugins.SettingTypeChoice && !optionAllowed(ms, decoded) {
		return Setting{}, false
	}
	s.Value = &decoded
	return s, true
}

// decode turns value_json into plain text; isSet is false for a blank,
// absent or unreadable value.
func decode(row Row) (text string, isSet bool) {
	if row.Unreadable || strings.TrimSpace(row.ValueJSON) == "" {
		return "", false
	}
	var str string
	if err := json.Unmarshal([]byte(row.ValueJSON), &str); err == nil {
		return str, str != ""
	}
	raw := strings.TrimSpace(row.ValueJSON)
	if raw == "null" || strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		return "", false
	}
	return raw, true // a bare JSON number or bool literal
}

func optionAllowed(ms plugins.ManifestSetting, v string) bool {
	for _, o := range ms.Options {
		if o.Value == v {
			return true
		}
	}
	return false
}

func urlHasCredentials(v string) bool {
	if v == "" {
		return false
	}
	u, err := url.Parse(v)
	return err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != ""
}

// urlValueCarriesCredentials: any value that is an absolute URL with a user,
// password, query or fragment (a webhook or image endpoint declared without
// type "endpoint" still often holds a token there). It never reports a value.
func urlValueCarriesCredentials(v string) bool {
	return strings.Contains(v, "://") && urlHasCredentials(v)
}

// printable: valid UTF-8, no control character, no format character except
// the zero-width (non-)joiners U+200C/U+200D, which Persian and Indic text
// needs. ut-cloud's claims.plainSettingText applies the same rule.
func printable(v string) bool {
	if !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) || r == unicode.ReplacementChar || (unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d') {
			return false
		}
	}
	return true
}

// Collect builds the report from this till's database. Local data only.
// An error means "omit the key" (keep the cloud's last report).
func Collect(ctx context.Context, db *sql.DB) ([]PluginEntry, error) {
	repo := data.NewPluginRepo(db)
	managed, err := repo.ListManagedPlugins(ctx)
	if err != nil {
		return nil, err
	}
	var in []Plugin
	for _, mp := range managed {
		if !mp.IsActive {
			continue
		}
		m, ok, err := plugins.InstalledManifest(ctx, db, mp.ID)
		if err != nil || !ok || len(m.Settings) == 0 {
			continue // an unreadable manifest reports nothing for that plugin
		}
		rows, err := repo.ListPluginSettings(ctx, mp.ID)
		if err != nil {
			return nil, err
		}
		p := Plugin{ID: mp.ID, Version: mp.Version, Settings: m.Settings, Rows: map[string]Row{}}
		for _, r := range rows {
			if r.Scope == "global" {
				p.Rows[r.Key] = Row{ValueJSON: r.ValueJSON, Unreadable: r.Unreadable, UpdatedAt: r.UpdatedAt}
			}
		}
		in = append(in, p)
	}
	return Build(in), nil
}
