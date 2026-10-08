// Package pluginview is the v1 plugin view document (ADR-0121 §7,
// ut-docs#3160; format: ut-docs reference/plugin-views.md): the JSON a
// plugin answers to ui.view.ask / ui.action.ask, its strict decoder and
// validator, and the render model core's own html/template partials
// (web/ui/partials/pluginview/) draw. A plugin never supplies HTML, script,
// CSS or a URL: only components from a fixed vocabulary, locale keys from
// its own bundle, literals core escapes, and action names core turns into
// posts to the entry's own route.
package pluginview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/universaltill/universal-till/internal/httpx"
)

// Version is the only document version this till renders.
const Version = 1

// Caps (reference/plugin-views.md "Limits"). A document over any of them
// is refused whole, never truncated.
const (
	MaxDocumentBytes = 256 << 10
	MaxComponents    = 200
	MaxStringBytes   = 4 << 10
	MaxTableRows     = 500
	MaxTableCols     = 20
	MaxTiles         = 24
	MaxListItems     = 200
	MaxFormFields    = 50
	MaxSelectOptions = 100
)

var (
	// nameRe: an action or form field name. A leading '_' is reserved for
	// core's own form fields (_action, _kind.<name>).
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,63}$`)
	// currencyRe: an ISO-4217 code's shape.
	currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
	// numberRe: a plain decimal, no exponent (no float round-trip anywhere).
	numberRe = regexp.MustCompile(`^-?[0-9]{1,18}(\.[0-9]{1,9})?$`)
)

// ValidName reports whether s is a valid action / field name.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Context is what the validator checks a document against: the answering
// plugin's own locale keys and its own page-entry routes.
type Context struct {
	OwnKeys   map[string]bool
	OwnRoutes []string
}

// Text is a locale key from the plugin's own bundle, or a literal core
// escapes — exactly one.
type Text struct {
	Key     string  `json:"key,omitempty"`
	Literal *string `json:"literal,omitempty"`
}

// Money is an amount in minor units with its currency.
type Money struct {
	Minor    int64  `json:"minor"`
	Currency string `json:"currency"`
}

// Cell is one table cell or stat-tile value: a JSON string (text), a JSON
// number (number) or {minor, currency} (money); null in a table is empty.
type Cell struct {
	Kind   string // "", "text", "number", "money"
	Text   string
	Number string
	Money  Money
}

// UnmarshalJSON decodes a cell by its JSON shape.
func (c *Cell) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0:
		return errors.New("empty cell")
	case string(b) == "null":
		*c = Cell{}
	case b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*c = Cell{Kind: "text", Text: s}
	case b[0] == '{':
		var m Money
		if err := strictDecode(b, &m); err != nil {
			return fmt.Errorf("money cell: %w", err)
		}
		*c = Cell{Kind: "money", Money: m}
	case b[0] == '-' || (b[0] >= '0' && b[0] <= '9'):
		if !numberRe.Match(b) {
			return fmt.Errorf("number cell %s must be a plain decimal (no exponent, ≤18 integer and ≤9 fraction digits)", b)
		}
		*c = Cell{Kind: "number", Number: string(b)}
	default:
		return fmt.Errorf("cell %s is not a string, number, money object or null", b)
	}
	return nil
}

// Component is one entry of a document's flat component list. Exactly the
// field matching Type is set.
type Component struct {
	Type       string
	Heading    *Heading
	Text       *TextBlock
	Notice     *Notice
	StatTiles  *StatTiles
	Table      *Table
	List       *List
	EmptyState *EmptyState
	Button     *Button
	Form       *Form
}

type Heading struct {
	Type string `json:"type"`
	Text Text   `json:"text"`
}

type TextBlock struct {
	Type string `json:"type"`
	Text Text   `json:"text"`
}

type Notice struct {
	Type  string `json:"type"`
	Level string `json:"level"`
	Text  Text   `json:"text"`
}

type Tile struct {
	Label Text `json:"label"`
	Value Cell `json:"value"`
}

type StatTiles struct {
	Type  string `json:"type"`
	Tiles []Tile `json:"tiles"`
}

type Column struct {
	Label Text   `json:"label"`
	Kind  string `json:"kind"`
}

type Table struct {
	Type    string   `json:"type"`
	Columns []Column `json:"columns"`
	Rows    [][]Cell `json:"rows"`
}

type List struct {
	Type  string `json:"type"`
	Items []Text `json:"items"`
}

type ActionRef struct {
	Action string `json:"action"`
	Label  Text   `json:"label"`
}

type EmptyState struct {
	Type   string     `json:"type"`
	Title  Text       `json:"title"`
	Body   *Text      `json:"body,omitempty"`
	Action *ActionRef `json:"action,omitempty"`
}

type Button struct {
	Type   string `json:"type"`
	Action string `json:"action"`
	Label  Text   `json:"label"`
	Style  string `json:"style,omitempty"`
}

type Option struct {
	Value string `json:"value"`
	Label Text   `json:"label"`
}

type Field struct {
	Name     string          `json:"name"`
	Label    Text            `json:"label"`
	Kind     string          `json:"kind"`
	Required bool            `json:"required,omitempty"`
	Options  []Option        `json:"options,omitempty"`
	Value    json.RawMessage `json:"value,omitempty"`
}

type Form struct {
	Type   string  `json:"type"`
	Action string  `json:"action"`
	Submit Text    `json:"submit"`
	Fields []Field `json:"fields"`
}

// Document is a v1 view document.
type Document struct {
	Version    int         `json:"version"`
	Title      *Text       `json:"title,omitempty"`
	Components []Component `json:"-"`
}

// rawDocument is Document as it arrives, components still undecoded.
type rawDocument struct {
	Version    int               `json:"version"`
	Title      *Text             `json:"title,omitempty"`
	Components []json.RawMessage `json:"components"`
}

type answer struct {
	Document *rawDocument `json:"document,omitempty"`
	Redirect *string      `json:"redirect,omitempty"`
}

// strictDecode decodes exactly one JSON value into v, refusing unknown
// fields and trailing data.
func strictDecode(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after JSON value")
	}
	return nil
}

func decodeAnswer(raw []byte, c Context) (*Document, *string, error) {
	if len(raw) > MaxDocumentBytes {
		return nil, nil, fmt.Errorf("answer exceeds %d bytes (%d)", MaxDocumentBytes, len(raw))
	}
	var a answer
	if err := strictDecode(raw, &a); err != nil {
		return nil, nil, fmt.Errorf("answer json: %w", err)
	}
	if a.Document != nil && a.Redirect != nil {
		return nil, nil, errors.New("answer has both document and redirect")
	}
	if a.Redirect != nil {
		return nil, a.Redirect, nil
	}
	if a.Document == nil {
		return nil, nil, errors.New("answer has no document")
	}
	d, err := buildDocument(a.Document, c)
	return d, nil, err
}

// DecodeViewAnswer decodes and validates a ui.view.ask answer:
// {"document": {...}}.
func DecodeViewAnswer(raw []byte, c Context) (*Document, error) {
	d, redirect, err := decodeAnswer(raw, c)
	if err != nil {
		return nil, err
	}
	if redirect != nil {
		return nil, errors.New("a ui.view.ask answer must be a document, not a redirect")
	}
	return d, nil
}

// DecodeActionAnswer decodes and validates a ui.action.ask answer: a
// document, or {"redirect": route} where route is exactly one of the same
// plugin's own page-entry routes.
func DecodeActionAnswer(raw []byte, c Context) (*Document, string, error) {
	d, redirect, err := decodeAnswer(raw, c)
	if err != nil {
		return nil, "", err
	}
	if redirect == nil {
		return d, "", nil
	}
	for _, r := range c.OwnRoutes {
		if r != "" && *redirect == r {
			return nil, r, nil
		}
	}
	return nil, "", fmt.Errorf("redirect %q is not one of this plugin's own page routes", *redirect)
}

func buildDocument(rd *rawDocument, c Context) (*Document, error) {
	if rd.Version != Version {
		return nil, fmt.Errorf("document version %d is not supported (want %d)", rd.Version, Version)
	}
	if len(rd.Components) > MaxComponents {
		return nil, fmt.Errorf("document has %d components (max %d)", len(rd.Components), MaxComponents)
	}
	v := validator{c: c}
	d := &Document{Version: rd.Version, Title: rd.Title}
	if rd.Title != nil {
		v.text("title", *rd.Title)
	}
	for i, raw := range rd.Components {
		comp, err := decodeComponent(raw)
		if err != nil {
			return nil, fmt.Errorf("components[%d]: %w", i, err)
		}
		v.component(fmt.Sprintf("components[%d]", i), comp)
		d.Components = append(d.Components, comp)
	}
	if v.err != nil {
		return nil, v.err
	}
	return d, nil
}

func decodeComponent(raw json.RawMessage) (Component, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Component{}, err
	}
	c := Component{Type: head.Type}
	var target any
	switch head.Type {
	case "heading":
		c.Heading = &Heading{}
		target = c.Heading
	case "text":
		c.Text = &TextBlock{}
		target = c.Text
	case "notice":
		c.Notice = &Notice{}
		target = c.Notice
	case "stat_tiles":
		c.StatTiles = &StatTiles{}
		target = c.StatTiles
	case "table":
		c.Table = &Table{}
		target = c.Table
	case "list":
		c.List = &List{}
		target = c.List
	case "empty_state":
		c.EmptyState = &EmptyState{}
		target = c.EmptyState
	case "button":
		c.Button = &Button{}
		target = c.Button
	case "form":
		c.Form = &Form{}
		target = c.Form
	default:
		return Component{}, fmt.Errorf("unknown component type %q", head.Type)
	}
	if err := strictDecode(raw, target); err != nil {
		return Component{}, fmt.Errorf("%s: %w", head.Type, err)
	}
	return c, nil
}

// validator collects the first error.
type validator struct {
	c   Context
	err error
}

func (v *validator) fail(format string, a ...any) {
	if v.err == nil {
		v.err = fmt.Errorf(format, a...)
	}
}

func (v *validator) str(where, s string) {
	if len(s) > MaxStringBytes {
		v.fail("%s exceeds %d bytes", where, MaxStringBytes)
	}
}

func (v *validator) text(where string, t Text) {
	switch {
	case (t.Key == "") == (t.Literal == nil):
		v.fail("%s must have exactly one of key or literal", where)
	case t.Literal != nil:
		v.str(where, *t.Literal)
	case !v.c.OwnKeys[t.Key]:
		v.fail("%s key %q is not in this plugin's own locale bundle", where, t.Key)
	}
}

func (v *validator) name(where, kind, s string) {
	if !nameRe.MatchString(s) {
		v.fail("%s %s %q must match [a-z0-9][a-z0-9_]{0,63}", where, kind, s)
	}
}

func (v *validator) cell(where string, c Cell, want string) {
	switch c.Kind {
	case "":
		return
	case "text":
		v.str(where, c.Text)
	case "money":
		if !currencyRe.MatchString(c.Money.Currency) {
			v.fail("%s currency %q must be an ISO-4217 code (three upper-case letters)", where, c.Money.Currency)
		} else if !httpx.IsKnownCurrency(c.Money.Currency) {
			// An unknown code would be formatted with a guessed 2 decimals
			// (KWD has 3: a 10x display error), so only the till's own
			// currency registry is accepted.
			v.fail("%s currency %q is not one the till knows", where, c.Money.Currency)
		}
	}
	if want != "" && c.Kind != want {
		v.fail("%s is a %s cell in a %s column (kind mismatch)", where, c.Kind, want)
	}
}

func (v *validator) component(where string, c Component) {
	switch c.Type {
	case "heading":
		v.text(where+".text", c.Heading.Text)
	case "text":
		v.text(where+".text", c.Text.Text)
	case "notice":
		switch c.Notice.Level {
		case "info", "warn", "error":
		default:
			v.fail("%s notice level %q must be info, warn or error", where, c.Notice.Level)
		}
		v.text(where+".text", c.Notice.Text)
	case "stat_tiles":
		if len(c.StatTiles.Tiles) > MaxTiles {
			v.fail("%s has %d tiles (max %d)", where, len(c.StatTiles.Tiles), MaxTiles)
		}
		for i, t := range c.StatTiles.Tiles {
			w := fmt.Sprintf("%s.tiles[%d]", where, i)
			v.text(w+".label", t.Label)
			v.cell(w+".value", t.Value, "")
		}
	case "table":
		t := c.Table
		if len(t.Columns) == 0 || len(t.Columns) > MaxTableCols {
			v.fail("%s has %d columns (1..%d)", where, len(t.Columns), MaxTableCols)
		}
		if len(t.Rows) > MaxTableRows {
			v.fail("%s has %d rows (max %d)", where, len(t.Rows), MaxTableRows)
		}
		for i, col := range t.Columns {
			w := fmt.Sprintf("%s.columns[%d]", where, i)
			switch col.Kind {
			case "text", "number", "money":
			default:
				v.fail("%s kind %q must be text, number or money", w, col.Kind)
			}
			v.text(w+".label", col.Label)
		}
		for i, row := range t.Rows {
			if len(row) != len(t.Columns) {
				v.fail("%s.rows[%d] has %d cells for %d columns", where, i, len(row), len(t.Columns))
				continue
			}
			for j, cell := range row {
				v.cell(fmt.Sprintf("%s.rows[%d][%d]", where, i, j), cell, t.Columns[j].Kind)
			}
		}
	case "list":
		if len(c.List.Items) > MaxListItems {
			v.fail("%s has %d items (max %d)", where, len(c.List.Items), MaxListItems)
		}
		for i, it := range c.List.Items {
			v.text(fmt.Sprintf("%s.items[%d]", where, i), it)
		}
	case "empty_state":
		e := c.EmptyState
		v.text(where+".title", e.Title)
		if e.Body != nil {
			v.text(where+".body", *e.Body)
		}
		if e.Action != nil {
			v.name(where+".action", "action", e.Action.Action)
			v.text(where+".action.label", e.Action.Label)
		}
	case "button":
		b := c.Button
		v.name(where, "action", b.Action)
		v.text(where+".label", b.Label)
		switch b.Style {
		case "", "primary", "secondary", "danger":
		default:
			v.fail("%s style %q must be primary, secondary or danger", where, b.Style)
		}
	case "form":
		v.form(where, c.Form)
	}
}

func (v *validator) form(where string, f *Form) {
	v.name(where, "action", f.Action)
	v.text(where+".submit", f.Submit)
	if len(f.Fields) > MaxFormFields {
		v.fail("%s has %d fields (max %d)", where, len(f.Fields), MaxFormFields)
	}
	seen := map[string]bool{}
	for i, fl := range f.Fields {
		w := fmt.Sprintf("%s.fields[%d]", where, i)
		v.name(w, "name", fl.Name)
		if seen[fl.Name] {
			v.fail("%s name %q used twice in one form", w, fl.Name)
		}
		seen[fl.Name] = true
		v.text(w+".label", fl.Label)
		if len(fl.Options) > 0 && fl.Kind != "select" {
			v.fail("%s options are only for a select field", w)
		}
		hasValue := len(fl.Value) > 0 && string(fl.Value) != "null"
		switch fl.Kind {
		case "text", "number":
			if hasValue {
				var s string
				if json.Unmarshal(fl.Value, &s) != nil {
					v.fail("%s value must be a string", w)
				}
				v.str(w+".value", s)
				if fl.Kind == "number" && s != "" && !numberRe.MatchString(s) {
					v.fail("%s value %q must be a plain decimal string", w, s)
				}
			}
		case "money":
			if hasValue {
				var n int64
				if !numberRe.Match(fl.Value) || bytes.ContainsRune(fl.Value, '.') || json.Unmarshal(fl.Value, &n) != nil {
					v.fail("%s value must be an integer amount in minor units", w)
				} else if n < 0 {
					// The money input only accepts an unsigned amount, so a
					// negative pre-fill could never be submitted back.
					v.fail("%s value must not be negative", w)
				}
			}
		case "toggle":
			if hasValue {
				var b bool
				if json.Unmarshal(fl.Value, &b) != nil {
					v.fail("%s value must be true or false", w)
				}
			}
		case "secret":
			if hasValue {
				v.fail("%s is a secret field: its value is never sent to the page", w)
			}
		case "select":
			if len(fl.Options) == 0 || len(fl.Options) > MaxSelectOptions {
				v.fail("%s select needs 1..%d options", w, MaxSelectOptions)
			}
			ok := !hasValue
			var val string
			if hasValue && json.Unmarshal(fl.Value, &val) != nil {
				v.fail("%s value must be a string", w)
			}
			for j, o := range fl.Options {
				v.str(fmt.Sprintf("%s.options[%d].value", w, j), o.Value)
				v.text(fmt.Sprintf("%s.options[%d].label", w, j), o.Label)
				if hasValue && o.Value == val {
					ok = true
				}
			}
			if !ok {
				v.fail("%s value %q is not one of its options", w, val)
			}
		default:
			v.fail("%s kind %q must be text, number, money, select, toggle or secret", w, fl.Kind)
		}
	}
}
