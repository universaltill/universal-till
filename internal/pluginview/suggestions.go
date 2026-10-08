package pluginview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// suggestions (ADR-0121 §7, ut-docs#3873; format: ut-docs
// reference/plugin-views.md): the one component that reaches a core
// screen. A list of candidates, each with a label, an optional detail and
// exactly one core-defined effect — add_to_basket {sku, qty} (core posts
// the SKU through the normal /api/pos/scan path) or apply_fields {field:
// value} onto a core form whose field names are a fixed allow-list. It
// only renders inside a core seam (Context.Seam); a plugin page or slot
// refuses it. A plugin never writes core data this way: it proposes, the
// operator commits.

// Seam names the core-owned screen a document is validated for. "" is a
// plugin page or content slot (no suggestions).
type Seam string

const (
	// SeamSellIdentify: the sell screen's camera identify overlay
	// (catalog.identify) — add_to_basket effects only.
	SeamSellIdentify Seam = "sell.identify"
	// SeamItemForm: the item form's "suggest details" (follow-up
	// ut-docs#3956) — apply_fields effects only.
	SeamItemForm Seam = "item.form"
)

// Caps on a suggestions component.
const (
	MaxSuggestions        = 20
	MaxSuggestionSKUBytes = 128
	MaxSuggestionQty      = 999
)

// itemFormFields is the item-form v1 apply_fields allow-list
// (reference/plugin-views.md): price is minor units, the rest strings.
var itemFormFields = []string{"name", "sku", "barcode", "category", "price"}

// ItemFormFields returns the item-form v1 apply_fields allow-list, as a
// fresh slice.
func ItemFormFields() []string { return append([]string(nil), itemFormFields...) }

// itemFormMoneyFields are the allow-list's minor-unit fields.
var itemFormMoneyFields = map[string]bool{"price": true}

func isItemFormField(name string) bool {
	for _, f := range itemFormFields {
		if f == name {
			return true
		}
	}
	return false
}

// seamComponents: what a document may hold in a seam — a form, button or
// table would post to a route the seam doesn't have.
var seamComponents = map[string]bool{"text": true, "notice": true, "suggestions": true}

// knownSeam reports whether s is a seam this till has.
func knownSeam(s Seam) bool {
	switch s {
	case "", SeamSellIdentify, SeamItemForm:
		return true
	}
	return false
}

type Suggestions struct {
	Type  string       `json:"type"`
	Items []Suggestion `json:"items"`
}

type Suggestion struct {
	Label  Text   `json:"label"`
	Detail *Text  `json:"detail,omitempty"`
	Effect Effect `json:"effect"`
}

// Effect is exactly one of AddToBasket or ApplyFields.
type Effect struct {
	AddToBasket *AddToBasket               `json:"add_to_basket,omitempty"`
	ApplyFields map[string]json.RawMessage `json:"apply_fields,omitempty"`
}

type AddToBasket struct {
	SKU string          `json:"sku"`
	Qty json.RawMessage `json:"qty,omitempty"`
}

// ViewSuggestion is one prepared candidate: SKU/Qty for add_to_basket,
// Fields (strings, price as int64 minor units) for apply_fields.
type ViewSuggestion struct {
	Label, Detail string
	SKU           string
	Qty           int
	Fields        map[string]any
}

// checkSeamAnswer refuses a redirect or job answer in a seam: the seam has
// no plugin route to go to.
func checkSeamAnswer(c Context, a answer) error {
	switch {
	case !knownSeam(c.Seam):
		return fmt.Errorf("unknown seam %q", c.Seam)
	case c.Seam == "":
		return nil
	case a.Redirect != nil:
		return fmt.Errorf("a redirect is not allowed in the %s seam", c.Seam)
	case a.Job != nil:
		return fmt.Errorf("a job is not allowed in the %s seam", c.Seam)
	}
	return nil
}

// seamComponent checks that a component may appear in v.c's seam.
func (v *validator) seamComponent(where, typ string) {
	if !knownSeam(v.c.Seam) {
		v.fail("%s: unknown seam %q", where, v.c.Seam)
		return
	}
	if v.c.Seam != "" && !seamComponents[typ] {
		v.fail("%s: a %s component is not allowed in the %s seam (text, notice and suggestions only)", where, typ, v.c.Seam)
	}
}

func (v *validator) suggestions(where string, s *Suggestions) {
	if v.c.Seam == "" {
		v.fail("%s: suggestions only render in a core seam, not on a plugin page or slot", where)
		return
	}
	if len(s.Items) == 0 || len(s.Items) > MaxSuggestions {
		v.fail("%s has %d items (1..%d)", where, len(s.Items), MaxSuggestions)
	}
	for i, it := range s.Items {
		w := fmt.Sprintf("%s.items[%d]", where, i)
		v.text(w+".label", it.Label)
		if it.Detail != nil {
			v.text(w+".detail", *it.Detail)
		}
		e := it.Effect
		if (e.AddToBasket != nil) == (e.ApplyFields != nil) {
			v.fail("%s.effect must have exactly one of add_to_basket or apply_fields", w)
			continue
		}
		if e.AddToBasket != nil {
			if v.c.Seam != SeamSellIdentify {
				v.fail("%s.effect add_to_basket is not allowed in the %s seam", w, v.c.Seam)
			}
			v.addToBasket(w+".effect.add_to_basket", e.AddToBasket)
			continue
		}
		if v.c.Seam != SeamItemForm {
			v.fail("%s.effect apply_fields is not allowed in the %s seam", w, v.c.Seam)
		}
		v.applyFields(w+".effect.apply_fields", e.ApplyFields)
	}
}

// hasControl reports invalid UTF-8 or a control character.
func hasControl(s string) bool {
	return !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0
}

func (v *validator) addToBasket(where string, a *AddToBasket) {
	switch {
	case a.SKU == "":
		v.fail("%s sku must not be empty", where)
	case len(a.SKU) > MaxSuggestionSKUBytes:
		v.fail("%s sku exceeds %d bytes", where, MaxSuggestionSKUBytes)
	case hasControl(a.SKU):
		v.fail("%s sku must not contain control characters", where)
	case strings.TrimSpace(a.SKU) != a.SKU:
		v.fail("%s sku must not have leading or trailing space", where)
	}
	if _, ok := suggestionQty(a.Qty); !ok {
		v.fail("%s qty must be an integer 1..%d", where, MaxSuggestionQty)
	}
}

// suggestionQty reads an add_to_basket qty: absent (or null) is 1.
func suggestionQty(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 1, true
	}
	var n int
	if !numberRe.Match(raw) || bytes.ContainsRune(raw, '.') || json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	return n, n >= 1 && n <= MaxSuggestionQty
}

func (v *validator) applyFields(where string, f map[string]json.RawMessage) {
	if len(f) == 0 {
		v.fail("%s must set at least one field", where)
		return
	}
	names := make([]string, 0, len(f))
	for k := range f {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		raw := f[k]
		if !isItemFormField(k) {
			v.fail("%s field %q is not on the item form's allow-list (%s)", where, k, strings.Join(itemFormFields, ", "))
			continue
		}
		if itemFormMoneyFields[k] {
			if _, ok := minorUnits(raw); !ok {
				v.fail("%s.%s must be a non-negative integer amount in minor units", where, k)
			}
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			v.fail("%s.%s must be a string", where, k)
			continue
		}
		v.str(where+"."+k, s)
		if hasControl(s) {
			v.fail("%s.%s must not contain control characters", where, k)
		}
	}
}

// minorUnits reads a non-negative integer amount in minor units (the
// money form field's grammar: no fraction, no exponent).
func minorUnits(raw json.RawMessage) (int64, bool) {
	var n int64
	if !numberRe.Match(raw) || bytes.ContainsRune(raw, '.') || json.Unmarshal(raw, &n) != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// prepareSuggestions resolves a suggestions component's candidates.
func prepareSuggestions(s *Suggestions, t func(Text) string) []ViewSuggestion {
	out := make([]ViewSuggestion, 0, len(s.Items))
	for _, it := range s.Items {
		vs := ViewSuggestion{Label: t(it.Label)}
		if it.Detail != nil {
			vs.Detail = t(*it.Detail)
		}
		if a := it.Effect.AddToBasket; a != nil {
			vs.SKU = a.SKU
			vs.Qty, _ = suggestionQty(a.Qty)
		}
		if f := it.Effect.ApplyFields; f != nil {
			vs.Fields = make(map[string]any, len(f))
			for k, raw := range f {
				if itemFormMoneyFields[k] {
					vs.Fields[k], _ = minorUnits(raw)
					continue
				}
				var str string
				_ = json.Unmarshal(raw, &str)
				vs.Fields[k] = str
			}
		}
		out = append(out, vs)
	}
	return out
}
