package pluginview

import (
	"fmt"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
)

// Caps on a submitted plugin view form (the handler also bounds the body).
const (
	MaxFormBytes  = 64 << 10
	MaxFormValues = 100
)

// Submission is a decoded plugin view form post: the action and the values
// core forwards to ui.action.ask.
type Submission struct {
	Action string
	// Values: text/select/secret/number fields as strings (number
	// normalised to a '.' decimal), money as Money in the till's currency,
	// toggle as bool.
	Values map[string]any
	// Invalid names fields whose typed value core could not read (a money
	// or number field outside its grammar); they are left out of Values.
	Invalid []string
	// Posted is the raw post as read, kept only to refill the form when
	// the action fails (View.Refill, ut-docs#3879); never sent anywhere.
	Posted map[string][]string
}

// DecodeForm reads a posted plugin view form. Core's own fields: _action
// (required) and _kind.<name> (money|number|toggle), which core's partials
// emit beside a typed field so the post can be read without the original
// document. They come from the operator's page, so they are hints: the
// plugin still validates every value it receives.
func DecodeForm(vals map[string][]string, decimals int, currency string) (Submission, error) {
	s := Submission{Values: map[string]any{}, Posted: vals}
	if len(vals) > MaxFormValues {
		return s, fmt.Errorf("form has %d fields (max %d)", len(vals), MaxFormValues)
	}
	first := func(k string) string {
		if v := vals[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	s.Action = first("_action")
	if !nameRe.MatchString(s.Action) {
		return s, fmt.Errorf("form _action %q is not an action name", s.Action)
	}
	kinds := map[string]string{}
	for k := range vals {
		if !strings.HasPrefix(k, "_") {
			continue
		}
		if k == "_action" {
			continue
		}
		name, ok := strings.CutPrefix(k, "_kind.")
		if !ok {
			return s, fmt.Errorf("form field %q is not a core field", k)
		}
		if !nameRe.MatchString(name) {
			return s, fmt.Errorf("form field %q names an invalid field", k)
		}
		switch kind := first(k); kind {
		case "money", "number", "toggle":
			kinds[name] = kind
		default:
			return s, fmt.Errorf("form field %q has unknown kind %q", k, kind)
		}
	}
	names := make([]string, 0, len(vals))
	for k := range vals {
		if !strings.HasPrefix(k, "_") {
			if !nameRe.MatchString(k) {
				return s, fmt.Errorf("form field %q is not a field name", k)
			}
			names = append(names, k)
		}
	}
	for name, kind := range kinds {
		if kind == "toggle" {
			s.Values[name] = false
		}
	}
	sort.Strings(names)
	for _, name := range names {
		raw := first(name)
		if len(raw) > MaxStringBytes {
			return s, fmt.Errorf("form field %q exceeds %d bytes", name, MaxStringBytes)
		}
		switch kinds[name] {
		case "money":
			if strings.TrimSpace(raw) == "" {
				continue
			}
			minor, err := httpx.ParseMoneyMajor(raw, decimals)
			if err != nil {
				s.Invalid = append(s.Invalid, name)
				continue
			}
			s.Values[name] = Money{Minor: minor, Currency: currency}
		case "number":
			n := strings.Replace(strings.TrimSpace(raw), ",", ".", 1)
			if n == "" {
				continue
			}
			if !numberRe.MatchString(n) {
				s.Invalid = append(s.Invalid, name)
				continue
			}
			s.Values[name] = n
		case "toggle":
			s.Values[name] = raw == "true"
		default:
			s.Values[name] = raw
		}
	}
	return s, nil
}
