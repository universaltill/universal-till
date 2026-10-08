package pluginview

import (
	"encoding/json"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
)

// View is the render model web/ui/partials/pluginview/*.html draws: every
// Text already resolved (key through the locale, literal as-is) and every
// number/amount already formatted — plain strings html/template escapes.
type View struct {
	Title      string
	Components []ViewComponent
}

// ViewComponent is one rendered component; only the fields of its Type are
// set.
type ViewComponent struct {
	Type string
	// heading, text, notice, empty_state body
	Text  string
	Level string // notice: info|warn|error
	Tiles []ViewTile
	// table
	Columns []ViewColumn
	Rows    [][]ViewCell
	// list
	Items []string
	// empty_state
	Title string
	// button / empty_state action / form
	Action string
	Label  string
	Style  string
	Submit string
	Fields []ViewField
	// Multipart: the form has a file field, so it posts
	// multipart/form-data (ut-docs#3793).
	Multipart bool
}

type ViewTile struct{ Label, Value string }

type ViewColumn struct{ Label, Kind string }

type ViewCell struct{ Value, Kind string }

type ViewOption struct {
	Value, Label string
	Selected     bool
}

type ViewField struct {
	Name, Label, Kind string
	Required          bool
	Value             string
	Checked           bool
	Options           []ViewOption
}

// Prepare resolves d for locale.
func (d *Document) Prepare(locale string) View {
	t := func(x Text) string {
		if x.Literal != nil {
			return *x.Literal
		}
		return httpx.T(locale, x.Key)
	}
	cell := func(c Cell) string {
		switch c.Kind {
		case "text":
			return c.Text
		case "number":
			return httpx.FormatDecimal(c.Number, locale)
		case "money":
			return httpx.FormatMoneyIn(money.FromMinor(c.Money.Minor).Minor(), c.Money.Currency, locale)
		}
		return ""
	}
	var v View
	if d.Title != nil {
		v.Title = t(*d.Title)
	}
	for _, c := range d.Components {
		vc := ViewComponent{Type: c.Type}
		switch c.Type {
		case "heading":
			vc.Text = t(c.Heading.Text)
		case "text":
			vc.Text = t(c.Text.Text)
		case "notice":
			vc.Level, vc.Text = c.Notice.Level, t(c.Notice.Text)
		case "stat_tiles":
			for _, tl := range c.StatTiles.Tiles {
				vc.Tiles = append(vc.Tiles, ViewTile{Label: t(tl.Label), Value: cell(tl.Value)})
			}
		case "table":
			for _, col := range c.Table.Columns {
				vc.Columns = append(vc.Columns, ViewColumn{Label: t(col.Label), Kind: col.Kind})
			}
			for _, row := range c.Table.Rows {
				r := make([]ViewCell, len(row))
				for i, cl := range row {
					r[i] = ViewCell{Value: cell(cl), Kind: c.Table.Columns[i].Kind}
				}
				vc.Rows = append(vc.Rows, r)
			}
		case "list":
			for _, it := range c.List.Items {
				vc.Items = append(vc.Items, t(it))
			}
		case "empty_state":
			vc.Title = t(c.EmptyState.Title)
			if c.EmptyState.Body != nil {
				vc.Text = t(*c.EmptyState.Body)
			}
			if a := c.EmptyState.Action; a != nil {
				vc.Action, vc.Label = a.Action, t(a.Label)
			}
		case "button":
			vc.Action, vc.Label, vc.Style = c.Button.Action, t(c.Button.Label), c.Button.Style
			if vc.Style == "" {
				vc.Style = "primary"
			}
		case "form":
			vc.Action, vc.Submit = c.Form.Action, t(c.Form.Submit)
			for _, f := range c.Form.Fields {
				vc.Fields = append(vc.Fields, prepareField(f, t))
				if f.Kind == "file" {
					vc.Multipart = true
				}
			}
		}
		v.Components = append(v.Components, vc)
	}
	return v
}

func prepareField(f Field, t func(Text) string) ViewField {
	vf := ViewField{Name: f.Name, Label: t(f.Label), Kind: f.Kind, Required: f.Required}
	hasValue := len(f.Value) > 0 && string(f.Value) != "null"
	switch f.Kind {
	case "text", "number", "select":
		if hasValue {
			_ = json.Unmarshal(f.Value, &vf.Value)
		}
	case "money":
		if hasValue {
			var n int64
			if json.Unmarshal(f.Value, &n) == nil {
				// Latin digits: the field is read back with ParseMoneyMajor.
				vf.Value = httpx.FormatMajorPlain(money.FromMinor(n).Minor(), httpx.ActiveCurrency().Decimals)
			}
		}
	case "toggle":
		if hasValue {
			_ = json.Unmarshal(f.Value, &vf.Checked)
		}
	case "secret", "file":
		// Never pre-filled (the validator refuses a value anyway).
	}
	for _, o := range f.Options {
		vf.Options = append(vf.Options, ViewOption{Value: o.Value, Label: t(o.Label), Selected: o.Value == vf.Value})
	}
	return vf
}
