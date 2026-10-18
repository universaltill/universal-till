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
// a choice has 1..maxSettingOptions options (0..maxSettingOptions with
// options_ask, ut-docs#4156) with unique non-empty values and non-empty
// labels, and a string default that is one of them; options appear only on
// a choice; label/help/option labels are bounded; show_when names another
// declared setting and at least one value. Shared by ParseManifest and
// VerifyManifest so both install paths refuse the same shapes; a `list`
// setting's record fields get the same rules from validateSettingsV2.
func validateSettingForm(m *Manifest) error {
	siblings := settingsByKey(m.Settings)
	for _, s := range m.Settings {
		if err := validateSettingDecl(fmt.Sprintf("manifest setting %q", s.Key), s, siblings); err != nil {
			return err
		}
	}
	return nil
}

// settingsByKey indexes one scope's settings (the top level, or one list's
// record fields) by key.
func settingsByKey(settings []ManifestSetting) map[string]ManifestSetting {
	out := make(map[string]ManifestSetting, len(settings))
	for _, s := range settings {
		out[s.Key] = s
	}
	return out
}

// visibleSibling reports whether key names another setting of the same
// scope whose value the page can read: declared, not s itself, never a
// secret (its value is never sent to the page), and a scalar (a list or
// map value is not one string).
func visibleSibling(self, key string, siblings map[string]ManifestSetting) bool {
	o, ok := siblings[key]
	return ok && key != self && o.Type != SettingTypeSecret && !IsSecretSettingKey(key) &&
		o.Type != SettingTypeList && o.Type != SettingTypeMap
}

// validateSettingDecl is validateSettingForm's per-setting check; where
// prefixes its errors and siblings is the setting's own scope.
func validateSettingDecl(where string, s ManifestSetting, siblings map[string]ManifestSetting) error {
	if len(s.Label) > maxSettingI18nKey || len(s.Help) > maxSettingI18nKey {
		return fmt.Errorf("%s: label/help longer than %d bytes", where, maxSettingI18nKey)
	}
	// A key the name heuristic treats as a credential is masked and sealed
	// whatever its type (ADR-0082), so it can only be a kind whose value is
	// one opaque string: never a dropdown, switch, number, list or map, and
	// it cannot drive show_when (its value never reaches the page).
	if IsSecretSettingKey(s.Key) && !credentialSettingType(s.Type) {
		return fmt.Errorf("%s: a credential-named key cannot be type %q", where, s.Type)
	}
	if s.Type != SettingTypeChoice && len(s.Options) > 0 {
		return fmt.Errorf("%s: options are only allowed on type %q", where, SettingTypeChoice)
	}
	if s.Type == SettingTypeChoice {
		lo := 1
		if s.OptionsAsk {
			lo = 0 // the plugin's ui.settings.options.ask fills the list (S5)
		}
		if len(s.Options) < lo || len(s.Options) > maxSettingOptions {
			return fmt.Errorf("%s: a choice needs %d..%d options, has %d", where, lo, maxSettingOptions, len(s.Options))
		}
		seen, err := validateSettingOptions(where, s.Options)
		if err != nil {
			return err
		}
		if s.DefaultValue != nil {
			dv, ok := s.DefaultValue.(string)
			if s.OptionsAsk {
				// Save accepts any value <= 64 bytes with no comma (S5).
				if !ok || len(dv) > maxSettingOptionValue || strings.Contains(dv, ",") {
					return fmt.Errorf("%s: default_value %v must be a string of at most %d bytes with no comma", where, s.DefaultValue, maxSettingOptionValue)
				}
			} else if !ok || (dv != "" && !seen[dv]) {
				return fmt.Errorf("%s: default_value %v is not one of its options", where, s.DefaultValue)
			}
		}
	}
	if w := s.ShowWhen; w != nil {
		if err := validateShowWhen(where, s.Key, w, siblings); err != nil {
			return err
		}
	}
	return nil
}

// validateSettingOptions checks a choice's options (a setting's, or a map
// value's) and returns the set of their values.
func validateSettingOptions(where string, opts []ManifestSettingOption) (map[string]bool, error) {
	seen := make(map[string]bool, len(opts))
	for _, o := range opts {
		if o.Value == "" || len(o.Value) > maxSettingOptionValue || o.Label == "" || len(o.Label) > maxSettingI18nKey {
			return nil, fmt.Errorf("%s: option %q needs a value of 1..%d bytes and a label of 1..%d bytes", where, o.Value, maxSettingOptionValue, maxSettingI18nKey)
		}
		if seen[o.Value] {
			return nil, fmt.Errorf("%s: duplicate option value %q", where, o.Value)
		}
		seen[o.Value] = true
	}
	return seen, nil
}

// validateShowWhen checks a setting's or a tab's show_when: the controlling
// setting is a visible sibling (visibleSibling; self is "" for a tab) and
// In holds 1..maxSettingOptions values the page can comma-join.
func validateShowWhen(where, self string, w *ManifestSettingCondition, siblings map[string]ManifestSetting) error {
	if !visibleSibling(self, w.Key, siblings) {
		return fmt.Errorf("%s: show_when.key %q must name another declared, non-secret setting", where, w.Key)
	}
	if len(w.In) == 0 || len(w.In) > maxSettingOptions {
		return fmt.Errorf("%s: show_when.in needs 1..%d values", where, maxSettingOptions)
	}
	// The page carries In comma-joined in a data attribute.
	for _, v := range w.In {
		if v == "" || len(v) > maxSettingOptionValue || strings.Contains(v, ",") {
			return fmt.Errorf("%s: show_when.in value %q must be 1..%d bytes with no comma", where, v, maxSettingOptionValue)
		}
	}
	return nil
}
