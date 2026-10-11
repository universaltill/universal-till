package plugins

import (
	"fmt"
	"strings"
)

// SettingTypeChoice (ut-docs#2832): the setting holds one of its manifest's
// Options values. The settings page renders it as a dropdown with each
// option's translated label, and refuses a saved value that is neither an
// option nor the value already stored (an installed shop's pre-existing
// value is kept, never silently rewritten). #4023's generic plugin settings
// form builds on this.
const SettingTypeChoice = "choice"

// Bounds on settings-form metadata: a manifest is untrusted input rendered
// into an admin page.
const (
	maxSettingOptions     = 32
	maxSettingOptionValue = 64
	maxSettingI18nKey     = 128
)

// SettingDecl returns the manifest's declaration of key, or nil when the
// manifest (nil-safe) does not declare it.
func (m *Manifest) SettingDecl(key string) *ManifestSetting {
	if m == nil {
		return nil
	}
	for i := range m.Settings {
		if m.Settings[i].Key == key {
			return &m.Settings[i]
		}
	}
	return nil
}

// ChoiceAllows reports whether v is exactly one of this setting's option
// values.
func (s *ManifestSetting) ChoiceAllows(v string) bool {
	if s == nil {
		return false
	}
	for _, o := range s.Options {
		if o.Value == v {
			return true
		}
	}
	return false
}

// validateSettingForm checks the settings-form metadata of every setting:
// a choice has 1..maxSettingOptions options with unique non-empty values
// and non-empty labels, and a string default that is one of them; options
// appear only on a choice; label/help/option labels are bounded; show_when
// names another declared setting and at least one value. Shared by
// ParseManifest and VerifyManifest so both install paths refuse the same
// shapes.
func validateSettingForm(m *Manifest) error {
	declared := make(map[string]bool, len(m.Settings))
	types := make(map[string]string, len(m.Settings))
	for _, s := range m.Settings {
		declared[s.Key] = true
		types[s.Key] = s.Type
	}
	for _, s := range m.Settings {
		if len(s.Label) > maxSettingI18nKey || len(s.Help) > maxSettingI18nKey {
			return fmt.Errorf("manifest setting %q: label/help longer than %d bytes", s.Key, maxSettingI18nKey)
		}
		// A key the name heuristic treats as a credential is masked and
		// sealed whatever its type (ADR-0082), so it can neither be a
		// dropdown nor drive show_when (its value never reaches the page).
		if s.Type == SettingTypeChoice && IsSecretSettingKey(s.Key) {
			return fmt.Errorf("manifest setting %q: a credential-named key cannot be type %q", s.Key, SettingTypeChoice)
		}
		if s.Type != SettingTypeChoice && len(s.Options) > 0 {
			return fmt.Errorf("manifest setting %q: options are only allowed on type %q", s.Key, SettingTypeChoice)
		}
		if s.Type == SettingTypeChoice {
			if len(s.Options) == 0 || len(s.Options) > maxSettingOptions {
				return fmt.Errorf("manifest setting %q: a choice needs 1..%d options, has %d", s.Key, maxSettingOptions, len(s.Options))
			}
			seen := make(map[string]bool, len(s.Options))
			for _, o := range s.Options {
				if o.Value == "" || len(o.Value) > maxSettingOptionValue || o.Label == "" || len(o.Label) > maxSettingI18nKey {
					return fmt.Errorf("manifest setting %q: option %q needs a value of 1..%d bytes and a label of 1..%d bytes", s.Key, o.Value, maxSettingOptionValue, maxSettingI18nKey)
				}
				if seen[o.Value] {
					return fmt.Errorf("manifest setting %q: duplicate option value %q", s.Key, o.Value)
				}
				seen[o.Value] = true
			}
			if s.DefaultValue != nil {
				dv, ok := s.DefaultValue.(string)
				if !ok || (dv != "" && !seen[dv]) {
					return fmt.Errorf("manifest setting %q: default_value %v is not one of its options", s.Key, s.DefaultValue)
				}
			}
		}
		if w := s.ShowWhen; w != nil {
			// The controlling setting must be a visible value the page can
			// read: never the field itself, never a secret (its value is
			// never sent to the page).
			if w.Key == s.Key || !declared[w.Key] || types[w.Key] == SettingTypeSecret || IsSecretSettingKey(w.Key) {
				return fmt.Errorf("manifest setting %q: show_when.key %q must name another declared, non-secret setting", s.Key, w.Key)
			}
			if len(w.In) == 0 || len(w.In) > maxSettingOptions {
				return fmt.Errorf("manifest setting %q: show_when.in needs 1..%d values", s.Key, maxSettingOptions)
			}
			// The page carries In comma-joined in a data attribute.
			for _, v := range w.In {
				if v == "" || len(v) > maxSettingOptionValue || strings.Contains(v, ",") {
					return fmt.Errorf("manifest setting %q: show_when.in value %q must be 1..%d bytes with no comma", s.Key, v, maxSettingOptionValue)
				}
			}
		}
	}
	return nil
}
