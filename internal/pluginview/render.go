package pluginview

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"sort"
	"strconv"
	"strings"

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
	// suggestions (ut-docs#3873)
	Suggestions []ViewSuggestion
	// qr (ut-docs#3874): a PNG data URI core encoded from the plugin's
	// data (never a plugin-supplied URL), its alt text, and the caption in
	// Label.
	QR     template.URL
	QRSize int // the image's edge in pixels
	Alt    string
	// wizard_step (ut-docs#3874): Text is "Step n of m".
	Steps []ViewStep
	// table paging (ut-docs#3874)
	Paging *ViewPaging
	// confirm (ut-docs#2838): the proposal id; Confirm is core's own card,
	// filled by ResolveConfirms (nil: no longer valid).
	Proposal string
	Confirm  *ViewConfirm
}

// ViewConfirm is core's confirm card for one AI-agent proposal: every
// string comes from core (the action's translated name, the formatted
// before/after values), none from the plugin.
type ViewConfirm struct {
	ID           string
	Title        string
	Rows         []ViewConfirmRow
	ConfirmLabel string
}

// ViewConfirmRow is one before/after row.
type ViewConfirmRow struct{ Item, Field, Before, After string }

// ResolveConfirms fills each confirm component's card through resolve
// (core's proposal store, bound to the signed-in operator); resolve
// returns nil for an unknown, expired or someone else's proposal.
func (v *View) ResolveConfirms(resolve func(id string) *ViewConfirm) {
	for i := range v.Components {
		if c := &v.Components[i]; c.Type == "confirm" && resolve != nil {
			c.Confirm = resolve(c.Proposal)
		}
	}
}

// ViewStep is one wizard step; State is done, current or upcoming.
type ViewStep struct{ Label, State string }

// ViewPaging is a paged table's indicator ("Page n of m") and, once
// View.LinkPaging ran, its Previous/Next links ("" = none).
type ViewPaging struct {
	Param            string
	Page, Pages      int64 // Pages 0: unknown total
	HasNext          bool
	Text             string
	PrevURL, NextURL string
}

// qrModulePixels: a negative size makes go-qrcode draw each module this
// many pixels wide, so even a 1024-byte code (109 modules) scans cleanly;
// the image is drawn at its own size, shrunk only to fit a narrow screen.
const qrModulePixels = -4

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
		case "suggestions":
			vc.Suggestions = prepareSuggestions(c.Suggestions, t)
		case "qr":
			// Encoded by the validator (never re-encoded per draw).
			vc.QR = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(c.QR.png))
			vc.QRSize = pngWidth(c.QR.png)
			vc.Alt = httpx.T(locale, "plugin.view.qr_alt")
			if c.QR.Label != nil {
				vc.Label = t(*c.QR.Label)
			}
		case "confirm":
			vc.Proposal = c.Confirm.Proposal
		case "wizard_step":
			w := c.WizardStep
			for i, st := range w.Steps {
				state := "upcoming"
				switch {
				case i+1 < w.Current:
					state = "done"
				case i+1 == w.Current:
					state = "current"
				}
				vc.Steps = append(vc.Steps, ViewStep{Label: t(st), State: state})
			}
			vc.Text = fmt.Sprintf(httpx.T(locale, "plugin.view.wizard_step_of"), localNumber(int64(w.Current), locale), localNumber(int64(len(w.Steps)), locale))
		}
		if c.Type == "table" && c.Table.Paging != nil {
			vc.Paging = preparePaging(c.Table.Paging, locale)
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

// Refill writes an operator's posted values back into the form whose
// Action is action, so a failed action re-shows what they typed (no-JS
// path, ut-docs#3879). posted is the raw form post (name -> values), read
// as typed — money and number too, even if invalid. A field the post does
// not name keeps the plugin's value, except a toggle: an unchecked box is
// never posted, so absent means off. A select only takes a value one of
// its options has. Secret and file fields are never refilled.
func (v *View) Refill(action string, posted map[string][]string) {
	if posted == nil {
		return
	}
	for ci := range v.Components {
		c := &v.Components[ci]
		if c.Type != "form" || c.Action != action {
			continue
		}
		for fi := range c.Fields {
			refillField(&c.Fields[fi], posted)
		}
	}
}

func refillField(f *ViewField, posted map[string][]string) {
	vals, ok := posted[f.Name]
	val := ""
	if ok && len(vals) > 0 {
		val = vals[0]
	}
	switch f.Kind {
	case "toggle":
		f.Checked = val == "true"
	case "text", "number", "money":
		if ok {
			f.Value = val
		}
	case "select":
		if !ok {
			return
		}
		known := false
		for _, o := range f.Options {
			known = known || o.Value == val
		}
		if !known {
			return
		}
		f.Value = val
		for i := range f.Options {
			f.Options[i].Selected = f.Options[i].Value == val
		}
	}
	// secret, file: never written back into the page.
}

// localNumber is n in the locale's digits and grouping.
func localNumber(n int64, locale string) string {
	return httpx.FormatDecimal(strconv.FormatInt(n, 10), locale)
}

func preparePaging(p *Paging, locale string) *ViewPaging {
	vp := &ViewPaging{Param: p.ParamName(), Page: p.Page}
	if p.Pages != nil {
		vp.Pages = *p.Pages
		vp.HasNext = p.Page < *p.Pages
		vp.Text = fmt.Sprintf(httpx.T(locale, "plugin.view.page_n_of"), localNumber(p.Page, locale), localNumber(*p.Pages, locale))
	} else {
		vp.HasNext = *p.HasNext
		vp.Text = fmt.Sprintf(httpx.T(locale, "plugin.view.page_n"), localNumber(p.Page, locale))
	}
	return vp
}

// LinkPaging gives every paged table its Previous/Next links: route (the
// entry's own) with params (the query the view was asked with, already
// bounded by the caller) and the table's own param set to the page
// before/after. A view drawn where a link has nowhere to go (a content
// slot panel, the read-only setup wizard) skips this and shows the
// indicator alone.
//
// A link never carries a reserved "_" key (core's own, such as the job
// poll's _job) and holds at most MaxParams keys: the table's param plus
// the first MaxParams-1 others by sorted key, so the next ask still
// forwards the page.
func (v *View) LinkPaging(route string, params map[string]string) {
	link := func(param string, page int64) string {
		keys := make([]string, 0, len(params))
		for k := range params {
			if k != param && k != "" && !strings.HasPrefix(k, "_") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		if len(keys) > MaxParams-1 {
			keys = keys[:MaxParams-1]
		}
		q := url.Values{}
		for _, k := range keys {
			q.Set(k, params[k])
		}
		q.Set(param, strconv.FormatInt(page, 10))
		u := url.URL{Path: route, RawQuery: q.Encode()}
		return u.String()
	}
	for i := range v.Components {
		p := v.Components[i].Paging
		if p == nil {
			continue
		}
		p.PrevURL, p.NextURL = "", ""
		if p.Page > 1 {
			p.PrevURL = link(p.Param, p.Page-1)
		}
		if p.HasNext {
			p.NextURL = link(p.Param, p.Page+1)
		}
	}
}

// pngWidth reads a PNG's width from its IHDR chunk (0 if b is not one).
func pngWidth(b []byte) int {
	if len(b) < 24 || string(b[1:4]) != "PNG" {
		return 0
	}
	return int(b[16])<<24 | int(b[17])<<16 | int(b[18])<<8 | int(b[19])
}
