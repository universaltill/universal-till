package pages

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Plugin settings form v2, S2 field kinds (ADR-0121 amendment "plugin
// settings form v2", ut-docs#4157): how each declared kind is shown from
// its stored value_json, and how a submitted value is validated and turned
// back into the kind's fixed stored shape — bool → JSON true/false,
// number/percent/money → a JSON integer (basis points for percent, minor
// units of the shop currency for money; `null` when an optional one is
// cleared, the same shape an install writes for a setting without a
// default), text → a JSON string.

// maxSettingInteger is the largest magnitude a number setting may hold: the
// largest integer a float64 (encoding/json's number type on the plugin
// side, and in a manifest's min/max/default) holds exactly.
const maxSettingInteger = 1 << 53

// settingFieldKind returns the S2 kind a declaration draws as its own
// control — bool, number, percent or money — or "" for every other kind
// (text and untyped draw the text box, secret/choice/endpoint keep their
// own branches, list/map stay the raw text box until S3/S4).
func settingFieldKind(decl *plugins.ManifestSetting) string {
	if decl == nil {
		return ""
	}
	switch decl.Type {
	case plugins.SettingTypeBool, plugins.SettingTypeNumber, plugins.SettingTypePercent, plugins.SettingTypeMoney:
		return decl.Type
	}
	return ""
}

// isTextKind reports whether a declaration is a text box with text
// constraints (required, max_len): "text" or untyped.
func isTextKind(decl *plugins.ManifestSetting) bool {
	return decl != nil && (decl.Type == "" || decl.Type == plugins.SettingTypeText)
}

// storedField is a bool/number/percent/money setting's stored value as the
// form shows it.
type storedField struct {
	Display string // the text box's value (number/percent/money)
	Checked bool   // the switch's state (bool)
	// Invalid: the stored value doesn't read as the declared kind (a key
	// whose type changed, a hand edit). Display then holds it as it is, the
	// page says so, and a save leaves it untouched unless the operator
	// changes it (ADR-0121 S2 "Changing a key's type").
	Invalid bool
}

// readStoredField reads valueJSON as kind. An unset value (no row value,
// JSON null, an empty string) is empty, never invalid. A JSON string that
// parses as the kind (an older text setting) reads as that value, so the
// next save rewrites it in the kind's shape.
func readStoredField(kind, valueJSON string, decimals int, locale string) storedField {
	raw := strings.TrimSpace(valueJSON)
	if raw == "" || raw == "null" {
		return storedField{}
	}
	var str string
	isString := json.Unmarshal([]byte(raw), &str) == nil
	if isString && strings.TrimSpace(str) == "" {
		return storedField{}
	}
	if kind == plugins.SettingTypeBool {
		var b bool
		if json.Unmarshal([]byte(raw), &b) == nil {
			return storedField{Checked: b}
		}
		if isString {
			if b, err := strconv.ParseBool(strings.ToLower(strings.TrimSpace(str))); err == nil {
				return storedField{Checked: b}
			}
			return storedField{Display: str, Invalid: true}
		}
		return storedField{Display: raw, Invalid: true}
	}
	var n int64
	if isString {
		var err error
		if n, err = parseSettingNumber(kind, str, decimals); err != nil {
			return storedField{Display: str, Invalid: true}
		}
	} else {
		var num json.Number
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&num) != nil {
			return storedField{Display: raw, Invalid: true}
		}
		v, err := strconv.ParseInt(num.String(), 10, 64)
		if err != nil || v > maxSettingInteger || v < -maxSettingInteger {
			return storedField{Display: raw, Invalid: true}
		}
		n = v
	}
	shown := formatSettingNumber(kind, n, decimals, locale)
	// A negative percent or amount can't be typed back in (the grammar is
	// non-negative), so it is shown but counts as invalid.
	return storedField{Display: shown, Invalid: n < 0 && kind != plugins.SettingTypeNumber}
}

// formatSettingNumber renders a stored integer for its text box: a number
// as is, a percent's basis points and an amount's minor units as a decimal
// in the locale's separator (LocalizeMajor — the box's reader accepts '.'
// or ',').
func formatSettingNumber(kind string, n int64, decimals int, locale string) string {
	switch kind {
	case plugins.SettingTypePercent:
		return httpx.LocalizeMajor(httpx.FormatMajorPlain(n, 2), locale)
	case plugins.SettingTypeMoney:
		return httpx.LocalizeMajor(httpx.FormatMajorPlain(n, decimals), locale)
	}
	return strconv.FormatInt(n, 10)
}

// parseSettingNumber reads a typed number/percent/money value into its
// stored integer: a whole number (optional sign) for number, basis points
// via httpx.ParsePercentBP for percent, minor units via
// httpx.ParseMoneyMajor at the shop currency's decimals for money. Both
// readers are integer-only, so exponents, NaN and Inf are refused.
func parseSettingNumber(kind, raw string, decimals int) (int64, error) {
	s := strings.TrimSpace(raw)
	switch kind {
	case plugins.SettingTypePercent, plugins.SettingTypeMoney:
		var n int64
		var err error
		if kind == plugins.SettingTypePercent {
			n, err = httpx.ParsePercentBP(s)
		} else {
			n, err = httpx.ParseMoneyMajor(s, decimals)
		}
		// Same bound the read side applies (readStoredField), so a saved
		// value never reloads as "invalid" (review finding).
		if err == nil && n > maxSettingInteger {
			return 0, fmt.Errorf("invalid number %q", raw)
		}
		return n, err
	}
	digits := strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	if digits == "" || !allASCIIDigits(digits) {
		return 0, fmt.Errorf("invalid number %q", raw)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n > maxSettingInteger || n < -maxSettingInteger {
		return 0, fmt.Errorf("invalid number %q", raw)
	}
	return n, nil
}

func allASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// formatSettingLimit renders a min/max limit for an error message in the
// shop's number format: a percent as a percent, an amount as money.
func formatSettingLimit(kind string, n int64, locale string) string {
	switch kind {
	case plugins.SettingTypePercent:
		return httpx.FormatPercentBP(n, locale)
	case plugins.SettingTypeMoney:
		return httpx.FormatMoney(n, locale)
	}
	// Plain digits: the number box refuses thousands separators and
	// non-Latin digits, so the limit is written the way it must be typed.
	return strconv.FormatInt(n, 10)
}

// fieldError is one field's validation failure: an i18n key under
// plugins.settings.error (or the takeaway editor's own) and, for the
// messages that name a limit, that limit already formatted.
type fieldError struct {
	key string
	arg string
}

// message translates the error for locale.
func (e fieldError) message(locale string) string {
	msg := httpx.T(locale, e.key)
	if e.arg != "" {
		return fmt.Sprintf(msg, e.arg)
	}
	return msg
}

func requiredError() *fieldError { return &fieldError{key: "plugins.settings.error.required"} }

// validateNumberField checks a submitted number/percent/money value against
// its declaration and returns the value_json to store. A blank value is
// `null` (cleared) unless the field is required.
func validateNumberField(decl *plugins.ManifestSetting, raw string, decimals int, locale string) (string, *fieldError) {
	if strings.TrimSpace(raw) == "" {
		if decl.Required {
			return "", requiredError()
		}
		return "null", nil
	}
	n, err := parseSettingNumber(decl.Type, raw, decimals)
	if err != nil {
		return "", &fieldError{key: "plugins.settings.error.number"}
	}
	if decl.Min != nil && n < *decl.Min {
		return "", &fieldError{key: "plugins.settings.error.min", arg: formatSettingLimit(decl.Type, *decl.Min, locale)}
	}
	if decl.Max != nil && n > *decl.Max {
		return "", &fieldError{key: "plugins.settings.error.max", arg: formatSettingLimit(decl.Type, *decl.Max, locale)}
	}
	return strconv.FormatInt(n, 10), nil
}

// validateTextField checks a text value's required and max_len (counted in
// characters, as the box's maxlength counts them) and returns its
// value_json. A nil decl (an undeclared key) has no constraints.
func validateTextField(decl *plugins.ManifestSetting, val, locale string) (string, *fieldError) {
	if decl != nil && decl.Required && val == "" {
		return "", requiredError()
	}
	if decl != nil && decl.MaxLen > 0 && utf8.RuneCountInString(val) > decl.MaxLen {
		return "", &fieldError{key: "plugins.settings.error.too_long", arg: httpx.FormatQty(float64(decl.MaxLen), locale)}
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return "", &fieldError{key: "plugins.settings.error.number"}
	}
	return string(raw), nil
}

// showWhenHidden reports whether a show_when condition hides its field
// while the controlling setting holds cur (ut-docs#2832): hidden unless cur
// is one of the listed values — except that a controlling choice holding
// a value outside its options (an older free-text entry) hides nothing,
// since the plugin may still use the dependent fields.
func showWhenHidden(m *plugins.Manifest, w *plugins.ManifestSettingCondition, cur string) bool {
	if w == nil {
		return false
	}
	for _, v := range w.In {
		if cur == v {
			return false
		}
	}
	ctl := m.SettingDecl(w.Key)
	return !(ctl != nil && ctl.Type == plugins.SettingTypeChoice && cur != "" && !ctl.ChoiceAllows(cur))
}

// submittedBool reads a switch from the form: present reports whether the
// form carried it (its presence marker, or the checkbox itself from a form
// without one); on is its state. A form with neither leaves it alone.
func submittedBool(form map[string][]string, key string) (on, present bool) {
	_, marker := form["present_setting_"+key]
	vals, has := form["setting_"+key]
	if !marker && !has {
		return false, false
	}
	on = has && len(vals) > 0 && strings.TrimSpace(vals[0]) != "" && strings.TrimSpace(vals[0]) != "0" && !strings.EqualFold(strings.TrimSpace(vals[0]), "false")
	return on, true
}

// controlValue is a setting's value as show_when compares it: the
// submitted one when the form carries it, else the stored one. A switch
// reads "true"/"false".
func controlValue(m *plugins.Manifest, form map[string][]string, stored map[string]string, key string) string {
	if form != nil {
		if decl := m.SettingDecl(key); decl != nil && decl.Type == plugins.SettingTypeBool {
			if on, ok := submittedBool(form, key); ok {
				return strconv.FormatBool(on)
			}
		} else if decl != nil && decl.Type == plugins.SettingTypeSecret {
			// A secret's blank box means "keep", so its submitted text
			// says nothing about its value: use the stored one.
		} else if vals, ok := form["setting_"+key]; ok && len(vals) > 0 {
			return strings.TrimSpace(vals[0])
		}
	}
	return stored[key]
}

// settingVisibility answers, for one render or one save, which settings and
// tabs are hidden (S1, ut-docs#4666) — the single place both
// buildPluginSettingViews and validatePluginSettings ask, so the form that
// is drawn and the form that is validated cannot disagree.
//
// A setting is skipped (not validated, not written) when its own show_when
// is unmet or the tab it sits in is hidden. A controlling setting that is
// itself skipped counts as holding its STORED value, not the submitted one:
// the operator cannot see or change it, so what they typed there must not
// decide what else shows (chained show_when, deferred from #4157's review).
// The recursion is bounded by one visit per setting: a cycle the manifest
// validation should already forbid reads the controller's current value.
type settingVisibility struct {
	m       *plugins.Manifest
	stored  map[string]string
	cur     map[string]string
	skipMo  map[string]bool // memo of skipped()
	working map[string]bool // keys being resolved: breaks a cycle
}

// newSettingVisibility prepares the checks for a manifest, the submitted
// form (nil on a GET) and the stored control values.
func newSettingVisibility(m *plugins.Manifest, form map[string][]string, stored map[string]string) *settingVisibility {
	return &settingVisibility{m: m, stored: stored, cur: controlValues(m, form, stored), skipMo: map[string]bool{}, working: map[string]bool{}}
}

// effective is the value of a controlling setting as show_when sees it.
func (v *settingVisibility) effective(key string) string {
	if v.skipped(key) {
		return v.stored[key]
	}
	return v.cur[key]
}

// fieldHidden reports whether a setting's own show_when hides it.
func (v *settingVisibility) fieldHidden(key string) bool {
	decl := v.m.SettingDecl(key)
	if decl == nil || decl.ShowWhen == nil {
		return false
	}
	return showWhenHidden(v.m, decl.ShowWhen, v.effective(decl.ShowWhen.Key))
}

// tabHidden reports whether a declared tab's show_when hides it.
func (v *settingVisibility) tabHidden(id string) bool {
	if v.m == nil || id == "" {
		return false
	}
	for i := range v.m.SettingsTabs {
		if t := v.m.SettingsTabs[i]; t.ID == id {
			return t.ShowWhen != nil && showWhenHidden(v.m, t.ShowWhen, v.effective(t.ShowWhen.Key))
		}
	}
	return false
}

// skipped reports whether a setting is hidden by its own show_when or by
// its tab: neither validated nor written.
func (v *settingVisibility) skipped(key string) bool {
	if r, ok := v.skipMo[key]; ok {
		return r
	}
	if v.working[key] {
		return false
	}
	v.working[key] = true
	r := v.fieldHidden(key)
	if !r {
		if decl := v.m.SettingDecl(key); decl != nil {
			r = v.tabHidden(decl.Tab)
		}
	}
	delete(v.working, key)
	v.skipMo[key] = r
	return r
}

// hideable reports whether a setting can ever be hidden (its own show_when
// or its tab's), i.e. whether a setting controlled by it needs the stored
// value to fall back on.
func (v *settingVisibility) hideable(key string) bool {
	decl := v.m.SettingDecl(key)
	if decl == nil {
		return false
	}
	if decl.ShowWhen != nil {
		return true
	}
	for i := range v.m.SettingsTabs {
		if t := v.m.SettingsTabs[i]; t.ID == decl.Tab {
			return t.ShowWhen != nil
		}
	}
	return false
}
