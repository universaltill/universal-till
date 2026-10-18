package plugins

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
)

// Plugin settings form v2 (ADR-0121 amendment "plugin settings form v2",
// S1–S8, ut-docs#4156): the setting kinds and manifest fields beyond
// #2832's choice/label/help/show_when. This file is the manifest schema's
// validation only — both install paths (ParseManifest, VerifyManifest) call
// validateSettingsV2; the settings page that draws them is a later #4023
// card. ut-cloud's pkg/manifest ports these rules for upload; this is the
// authoritative copy.

// Setting kinds added by S2 (stored value_json shape in brackets): text
// [string], bool [true/false], number [integer], percent [integer basis
// points], money [integer minor units of the shop currency], list [S3,
// string-wrapped JSON array of records], map [S4, string-wrapped JSON
// object keyed by a core record id].
const (
	SettingTypeText    = "text"
	SettingTypeBool    = "bool"
	SettingTypeNumber  = "number"
	SettingTypePercent = "percent"
	SettingTypeMoney   = "money"
	SettingTypeList    = "list"
	SettingTypeMap     = "map"
)

// SettingMapValueTypes are the kinds a `map` setting's value may be (S4).
var SettingMapValueTypes = []string{
	SettingTypeText, SettingTypeNumber, SettingTypePercent, SettingTypeMoney, SettingTypeBool, SettingTypeChoice,
}

// SettingMapOver is the closed list of core records a `map` setting can
// key its values by (S4): records every shop has, so naming them is
// neutral (ADR-0121 §11).
var SettingMapOver = []string{"tax_codes", "payment_methods"}

// Bounds on the v2 settings form (S8): a manifest is untrusted input drawn
// into an admin page.
const (
	maxManifestSettings   = 64
	maxSettingsTabs       = 8
	maxListFields         = 12
	maxListItems          = 20
	maxSettingsTests      = 4
	maxSetupSteps         = 8
	maxSetupStepFields    = 64
	maxSettingMaxLen      = 4096
	maxSettingGuideURL    = 512
	maxSettingDependsOn   = 8
	maxSettingIntegerJSON = 1 << 53 // largest integer a float64 default holds exactly
)

// settingsFormIDRe is the shape of a settings_tabs / settings_tests id.
var settingsFormIDRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func settingTypeIn(t string, types ...string) bool {
	for _, v := range types {
		if t == v {
			return true
		}
	}
	return false
}

// credentialSettingType reports whether a credential-named key
// (IsSecretSettingKey) may have type t: only kinds whose value is one
// opaque string the page can mask and internal/data can seal.
func credentialSettingType(t string) bool {
	return settingTypeIn(t, "", SettingTypeText, SettingTypeSecret, SettingTypeEndpoint)
}

func isNumericSettingType(t string) bool {
	return settingTypeIn(t, SettingTypeNumber, SettingTypePercent, SettingTypeMoney)
}

// validateSettingsV2 refuses a manifest whose v2 settings-form fields are
// malformed: the S8 bounds, S1 tabs, the S2 per-kind constraints and
// default_value shapes, S3 list records (each field checked with the same
// per-setting rules), S4 maps, S5 asked options and tests, S6 guidance and
// the S7 setup flow. Shared by ParseManifest and VerifyManifest; runs after
// validateSettingForm (#2832's rules, which it extends to record fields).
func validateSettingsV2(m *Manifest) error {
	if len(m.Settings) > maxManifestSettings {
		return fmt.Errorf("manifest declares %d settings, at most %d are allowed", len(m.Settings), maxManifestSettings)
	}
	siblings := settingsByKey(m.Settings)

	if len(m.SettingsTabs) > maxSettingsTabs {
		return fmt.Errorf("manifest declares %d settings_tabs, at most %d are allowed", len(m.SettingsTabs), maxSettingsTabs)
	}
	tabs := make(map[string]bool, len(m.SettingsTabs))
	for i, tab := range m.SettingsTabs {
		where := fmt.Sprintf("manifest settings_tabs[%d]", i)
		if !settingsFormIDRe.MatchString(tab.ID) {
			return fmt.Errorf("%s: id %q must match %s", where, tab.ID, settingsFormIDRe)
		}
		if tabs[tab.ID] {
			return fmt.Errorf("%s: duplicate id %q", where, tab.ID)
		}
		tabs[tab.ID] = true
		if tab.Label == "" || len(tab.Label) > maxSettingI18nKey || len(tab.Help) > maxSettingI18nKey {
			return fmt.Errorf("%s: needs a label of 1..%d bytes and a help of at most %d bytes", where, maxSettingI18nKey, maxSettingI18nKey)
		}
		if tab.ShowWhen != nil {
			if err := validateShowWhen(where, "", tab.ShowWhen, siblings); err != nil {
				return err
			}
		}
	}

	hasList := false
	for _, s := range m.Settings {
		if err := validateSettingV2Decl(fmt.Sprintf("manifest setting %q", s.Key), s, siblings, tabs, false); err != nil {
			return err
		}
		if s.Type == SettingTypeList {
			hasList = true
		}
	}

	if len(m.SettingsTests) > maxSettingsTests {
		return fmt.Errorf("manifest declares %d settings_tests, at most %d are allowed", len(m.SettingsTests), maxSettingsTests)
	}
	tests := make(map[string]ManifestSettingsTest, len(m.SettingsTests))
	for i, st := range m.SettingsTests {
		where := fmt.Sprintf("manifest settings_tests[%d]", i)
		if !settingsFormIDRe.MatchString(st.ID) {
			return fmt.Errorf("%s: id %q must match %s", where, st.ID, settingsFormIDRe)
		}
		if _, dup := tests[st.ID]; dup {
			return fmt.Errorf("%s: duplicate id %q", where, st.ID)
		}
		tests[st.ID] = st
		if st.Label == "" || len(st.Label) > maxSettingI18nKey {
			return fmt.Errorf("%s: needs a label of 1..%d bytes", where, maxSettingI18nKey)
		}
		if st.Tab != "" && !tabs[st.Tab] {
			return fmt.Errorf("%s: tab %q is not a declared settings_tabs id", where, st.Tab)
		}
		if st.Record {
			// The button lives in each list record's panel, not on a tab.
			if st.Tab != "" {
				return fmt.Errorf("%s: a record test cannot also name a tab", where)
			}
			if !hasList {
				return fmt.Errorf("%s: a record test needs a %q setting", where, SettingTypeList)
			}
		}
	}

	if su := m.SettingsSetup; su != nil {
		if len(su.Steps) == 0 || len(su.Steps) > maxSetupSteps {
			return fmt.Errorf("manifest settings_setup: needs 1..%d steps, has %d", maxSetupSteps, len(su.Steps))
		}
		for i, step := range su.Steps {
			where := fmt.Sprintf("manifest settings_setup.steps[%d]", i)
			if step.Title == "" || len(step.Title) > maxSettingI18nKey || len(step.Help) > maxSettingI18nKey {
				return fmt.Errorf("%s: needs a title of 1..%d bytes and a help of at most %d bytes", where, maxSettingI18nKey, maxSettingI18nKey)
			}
			if len(step.Fields) == 0 || len(step.Fields) > maxSetupStepFields {
				return fmt.Errorf("%s: needs 1..%d fields, has %d", where, maxSetupStepFields, len(step.Fields))
			}
			seen := make(map[string]bool, len(step.Fields))
			for _, k := range step.Fields {
				if _, ok := siblings[k]; !ok {
					return fmt.Errorf("%s: field %q is not a declared top-level setting", where, k)
				}
				if seen[k] {
					return fmt.Errorf("%s: duplicate field %q", where, k)
				}
				seen[k] = true
			}
			if step.Test != "" {
				if st, ok := tests[step.Test]; !ok || st.Record {
					return fmt.Errorf("%s: test %q must name a declared, non-record settings_tests id", where, step.Test)
				}
			}
		}
	}
	return nil
}

// validateSettingV2Decl checks one setting's v2 fields: a top-level one
// (inRecord false; tabs are the declared tab ids) or a list record's field
// (inRecord true; siblings are the record's other fields).
func validateSettingV2Decl(where string, s ManifestSetting, siblings map[string]ManifestSetting, tabs map[string]bool, inRecord bool) error {
	if len(s.Placeholder) > maxSettingI18nKey || len(s.Guide) > maxSettingI18nKey || len(s.Notice) > maxSettingI18nKey {
		return fmt.Errorf("%s: placeholder/guide/notice longer than %d bytes", where, maxSettingI18nKey)
	}
	if s.Tab != "" {
		if inRecord {
			return fmt.Errorf("%s: a record field cannot name a tab", where)
		}
		if !tabs[s.Tab] {
			return fmt.Errorf("%s: tab %q is not a declared settings_tabs id", where, s.Tab)
		}
	}

	// Which constraint applies to which kind (S2).
	textLike := settingTypeIn(s.Type, "", SettingTypeText)
	if err := validateSettingBounds(where, s.Type, s.Min, s.Max, s.MaxLen, textLike); err != nil {
		return err
	}
	if s.Required && settingTypeIn(s.Type, SettingTypeBool, SettingTypeList, SettingTypeMap) {
		return fmt.Errorf("%s: required is not allowed on type %q", where, s.Type)
	}
	if s.Placeholder != "" && !textLike && !isNumericSettingType(s.Type) && !settingTypeIn(s.Type, SettingTypeSecret, SettingTypeEndpoint) {
		return fmt.Errorf("%s: placeholder is not allowed on type %q", where, s.Type)
	}
	if s.Paste && !textLike && s.Type != SettingTypeSecret {
		return fmt.Errorf("%s: paste is only allowed on a text or %q setting", where, SettingTypeSecret)
	}
	if s.Type != SettingTypeList && (len(s.Fields) > 0 || s.TitleField != "" || s.MinItems != 0 || s.MaxItems != 0) {
		return fmt.Errorf("%s: fields/title_field/min_items/max_items are only allowed on type %q", where, SettingTypeList)
	}
	if s.Type != SettingTypeMap && (s.MapOver != "" || s.Value != nil) {
		return fmt.Errorf("%s: map_over/value are only allowed on type %q", where, SettingTypeMap)
	}
	if s.Type != SettingTypeChoice && (s.OptionsAsk || len(s.DependsOn) > 0) {
		return fmt.Errorf("%s: options_ask/depends_on are only allowed on type %q", where, SettingTypeChoice)
	}

	if len(s.DependsOn) > 0 {
		if !s.OptionsAsk {
			return fmt.Errorf("%s: depends_on needs options_ask", where)
		}
		if len(s.DependsOn) > maxSettingDependsOn {
			return fmt.Errorf("%s: depends_on names %d settings, at most %d", where, len(s.DependsOn), maxSettingDependsOn)
		}
		seen := make(map[string]bool, len(s.DependsOn))
		for _, k := range s.DependsOn {
			if !visibleSibling(s.Key, k, siblings) || seen[k] {
				return fmt.Errorf("%s: depends_on %q must name another declared, non-secret setting, once", where, k)
			}
			seen[k] = true
		}
	}

	if s.GuideURL != "" && !validGuideURL(s.GuideURL) {
		return fmt.Errorf("%s: guide_url must be an https:// URL with a host and no userinfo, at most %d bytes", where, maxSettingGuideURL)
	}

	if err := validateSettingDefaultShape(where, s, inRecord); err != nil {
		return err
	}

	switch s.Type {
	case SettingTypeList:
		return validateListSetting(where, s)
	case SettingTypeMap:
		if !settingTypeIn(s.MapOver, SettingMapOver...) {
			return fmt.Errorf("%s: map_over %q must be one of %s", where, s.MapOver, strings.Join(SettingMapOver, "|"))
		}
		if s.Value == nil {
			return fmt.Errorf("%s: a map needs a value", where)
		}
		return validateMapValue(where, s.Value)
	}
	return nil
}

// validateSettingBounds checks min/max (numeric kinds only, min <= max) and
// max_len (maxLenOK kinds only, 1..maxSettingMaxLen) — shared by a setting
// and a map's value.
func validateSettingBounds(where, typ string, lo, hi *int64, maxLen int, maxLenOK bool) error {
	if (lo != nil || hi != nil) && !isNumericSettingType(typ) {
		return fmt.Errorf("%s: min/max are only allowed on type %s|%s|%s", where, SettingTypeNumber, SettingTypePercent, SettingTypeMoney)
	}
	if lo != nil && hi != nil && *lo > *hi {
		return fmt.Errorf("%s: min %d is greater than max %d", where, *lo, *hi)
	}
	if maxLen != 0 {
		if !maxLenOK {
			return fmt.Errorf("%s: max_len is only allowed on a text setting", where)
		}
		if maxLen < 1 || maxLen > maxSettingMaxLen {
			return fmt.Errorf("%s: max_len must be 1..%d, is %d", where, maxSettingMaxLen, maxLen)
		}
	}
	return nil
}

// validateListSetting checks a `list`'s records (S3): 1..maxListFields
// fields, each a non-list/map setting validated with the same per-setting
// rules within the record's own scope; a title_field naming a text or
// choice sibling; 0 <= min_items <= max_items, max_items 1..maxListItems.
func validateListSetting(where string, s ManifestSetting) error {
	if len(s.Fields) == 0 || len(s.Fields) > maxListFields {
		return fmt.Errorf("%s: a list needs 1..%d fields, has %d", where, maxListFields, len(s.Fields))
	}
	fields := make(map[string]ManifestSetting, len(s.Fields))
	for _, f := range s.Fields {
		if f.Key == "" {
			return fmt.Errorf("%s: a field needs a key", where)
		}
		if _, dup := fields[f.Key]; dup {
			return fmt.Errorf("%s: duplicate field key %q", where, f.Key)
		}
		fields[f.Key] = f
	}
	for _, f := range s.Fields {
		fwhere := fmt.Sprintf("%s field %q", where, f.Key)
		if !isValidSettingType(f.Type) {
			return fmt.Errorf("%s has invalid type %q (allowed: %s)", fwhere, f.Type, allowedSettingTypes())
		}
		if f.Type == SettingTypeList || f.Type == SettingTypeMap {
			return fmt.Errorf("%s: a record field cannot be type %q", fwhere, f.Type)
		}
		if err := validateSettingDecl(fwhere, f, fields); err != nil {
			return err
		}
		if err := validateSettingV2Decl(fwhere, f, fields, nil, true); err != nil {
			return err
		}
	}
	tf, ok := fields[s.TitleField]
	if !ok || !settingTypeIn(tf.Type, "", SettingTypeText, SettingTypeChoice) || IsSecretSettingKey(tf.Key) {
		return fmt.Errorf("%s: title_field %q must name a non-secret text or choice field", where, s.TitleField)
	}
	if s.MaxItems < 1 || s.MaxItems > maxListItems {
		return fmt.Errorf("%s: max_items must be 1..%d, is %d", where, maxListItems, s.MaxItems)
	}
	if s.MinItems < 0 || s.MinItems > s.MaxItems {
		return fmt.Errorf("%s: min_items must be 0..max_items (%d), is %d", where, s.MaxItems, s.MinItems)
	}
	return nil
}

// validateMapValue checks a `map`'s value (S4): a map-value kind with that
// kind's constraints, and a choice's options as a setting's.
func validateMapValue(where string, v *ManifestSettingMapValue) error {
	vwhere := where + " value"
	if !settingTypeIn(v.Type, SettingMapValueTypes...) {
		return fmt.Errorf("%s: type %q must be one of %s", vwhere, v.Type, strings.Join(SettingMapValueTypes, "|"))
	}
	if err := validateSettingBounds(vwhere, v.Type, v.Min, v.Max, v.MaxLen, v.Type == SettingTypeText); err != nil {
		return err
	}
	if v.Type != SettingTypeChoice {
		if len(v.Options) > 0 {
			return fmt.Errorf("%s: options are only allowed on type %q", vwhere, SettingTypeChoice)
		}
		return nil
	}
	if len(v.Options) == 0 || len(v.Options) > maxSettingOptions {
		return fmt.Errorf("%s: a choice needs 1..%d options, has %d", vwhere, maxSettingOptions, len(v.Options))
	}
	_, err := validateSettingOptions(vwhere, v.Options)
	return err
}

// validateSettingDefaultShape refuses a default_value that does not have
// its kind's stored shape (S2). An untyped ("") or secret top-level setting
// keeps today's any-JSON default (ut-plugin-tax-de ships object defaults on
// untyped keys; TestParseManifest_EndpointDefaultValue pins a numeric
// secret default), so an installed manifest still parses; a top-level
// choice is validateSettingForm's and a top-level endpoint
// validateSettingDefaults' job. Record fields (new in v2) get the strict
// shape for every kind.
func validateSettingDefaultShape(where string, s ManifestSetting, inRecord bool) error {
	dv := s.DefaultValue
	if dv == nil {
		return nil
	}
	switch s.Type {
	case "", SettingTypeSecret:
		if !inRecord {
			if str, ok := dv.(string); ok && s.MaxLen > 0 && len(str) > s.MaxLen {
				return fmt.Errorf("%s: default_value is longer than max_len %d", where, s.MaxLen)
			}
			return nil
		}
		fallthrough
	case SettingTypeText:
		str, ok := dv.(string)
		if !ok {
			return fmt.Errorf("%s: default_value %v must be a string", where, dv)
		}
		if s.MaxLen > 0 && len(str) > s.MaxLen {
			return fmt.Errorf("%s: default_value is longer than max_len %d", where, s.MaxLen)
		}
	case SettingTypeEndpoint:
		if inRecord {
			str, ok := dv.(string)
			if !ok || (str != "" && !ValidEndpointURL(str)) {
				return fmt.Errorf("%s: default_value %v must be an http(s) URL string", where, dv)
			}
		}
	case SettingTypeList:
		return fmt.Errorf("%s: a list cannot declare a default_value", where)
	case SettingTypeMap:
		obj, ok := dv.(map[string]interface{})
		if !ok {
			return fmt.Errorf("%s: default_value must be a JSON object", where)
		}
		if s.Value == nil {
			return nil // reported by the map checks
		}
		for k, v := range obj {
			if err := mapValueShape(s.Value, v); err != nil {
				return fmt.Errorf("%s: default_value[%q] %v", where, k, err)
			}
		}
	case SettingTypeBool, SettingTypeNumber, SettingTypePercent, SettingTypeMoney:
		if err := scalarValueShape(s.Type, s.Min, s.Max, dv); err != nil {
			return fmt.Errorf("%s: default_value %v", where, err)
		}
	}
	return nil
}

// mapValueShape checks one entry of a map default against its value kind.
func mapValueShape(v *ManifestSettingMapValue, x interface{}) error {
	switch v.Type {
	case SettingTypeText:
		str, ok := x.(string)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		if v.MaxLen > 0 && len(str) > v.MaxLen {
			return fmt.Errorf("is longer than max_len %d", v.MaxLen)
		}
		return nil
	case SettingTypeChoice:
		str, ok := x.(string)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		for _, o := range v.Options {
			if o.Value == str {
				return nil
			}
		}
		return fmt.Errorf("is not one of its options")
	}
	return scalarValueShape(v.Type, v.Min, v.Max, x)
}

// scalarValueShape checks a bool or numeric value: bool → JSON boolean;
// number/percent/money → a JSON integer (a float64 with no fraction, at
// most 2^53 in magnitude) within min/max when set.
func scalarValueShape(typ string, lo, hi *int64, x interface{}) error {
	if typ == SettingTypeBool {
		if _, ok := x.(bool); !ok {
			return fmt.Errorf("must be true or false")
		}
		return nil
	}
	n, ok := jsonInteger(x)
	if !ok {
		return fmt.Errorf("must be an integer")
	}
	if lo != nil && n < *lo {
		return fmt.Errorf("is below min %d", *lo)
	}
	if hi != nil && n > *hi {
		return fmt.Errorf("is above max %d", *hi)
	}
	return nil
}

// jsonInteger returns x as an int64 when it is a whole number a float64
// holds exactly (encoding/json decodes every number into a float64).
func jsonInteger(x interface{}) (int64, bool) {
	switch v := x.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || math.Abs(v) > maxSettingIntegerJSON {
			return 0, false
		}
		return int64(v), true
	case int:
		return int64(v), int64(v) >= -maxSettingIntegerJSON && int64(v) <= maxSettingIntegerJSON
	case int64:
		return v, v >= -maxSettingIntegerJSON && v <= maxSettingIntegerJSON
	}
	return 0, false
}

// validGuideURL reports whether v is an https:// URL with a host, no
// userinfo and no surrounding space, at most maxSettingGuideURL bytes (S6).
func validGuideURL(v string) bool {
	if len(v) > maxSettingGuideURL || v != strings.TrimSpace(v) {
		return false
	}
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "https" && u.Opaque == "" && u.Host != "" && u.Hostname() != "" && u.User == nil
}
