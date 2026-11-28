// Package shoptemplate is the shop-type template engine (ADR-0190): neutral
// data that says, per shop type, which Settings cards and Menu tiles are
// essential, common or tucked away, which labels/order a type uses, and
// which few defaults to seed once. It is a leaf: it imports shoptype and
// uislot only, never pages or data, and core has no `if shopType == ...`
// anywhere (scripts/ci/coreneutral scans this package and the JSON).
//
// A template never removes a row (ADR-0190): demotion is a Class read by
// the Settings nav, and a hidden Menu tile is a core.template amendment the
// shop can restore. Protected/statutory rows are fixed-class and a template
// naming them with a class fails to load.
//
// Resolution is precomputed at Load: ForType is a map read, ClassFor a map
// read, Amendments a slice return; the unset/unknown-type path allocates
// nothing (nil *Resolved is "no template" and every method is nil-safe).
package shoptemplate

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"

	"github.com/universaltill/universal-till/internal/uislot"
)

//go:embed templates/*.json
var embedded embed.FS

// SchemaVersion is the only template schema this build reads.
const SchemaVersion = 1

// PluginID stamps the amendments a template produces; a real plugin id never
// collides with it (no dot-free first-party id), and plugin amendments are
// appended after these so they win on restructure (design-A 2.4).
const PluginID = "core.template"

// Kind names the row family a key belongs to.
type Kind string

const (
	KindSettings Kind = "settings"
	KindMenu     Kind = "menu"
	KindItems    Kind = "items"
	KindRail     Kind = "rail"
)

// Slot names (equal to the uislot slot constants).
const (
	slotMenu     = uislot.MenuSlot
	slotItems    = uislot.ItemsSlot
	slotRail     = uislot.RailSlot
	slotSettings = uislot.SettingsSlot
)

// Catalog is what the engine validates a template against; the caller
// injects it so this package depends on neither pages nor data.
type Catalog interface {
	// HasKey: the CoreSettings/CoreMenu/CoreItems/CoreRail row exists.
	HasKey(kind Kind, key string) bool
	// FixedClass: the row is protected/statutory; a template may not give it a class.
	FixedClass(kind Kind, key string) bool
	// RegistryTier is the till-settings-registry tier of a setting key.
	RegistryTier(settingKey string) (tier string, ok bool)
	// CapabilityKnown: the manifest capability closed set.
	CapabilityKnown(capability string) bool
	// EnKey: web/locales/en.json has the key.
	EnKey(key string) bool
}

// Seed is one apply-once default.
type Seed struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Value is one template entry for a row: a bare string is a class
// ("essential"|"common"|"advanced"); an object carries amendment fields and
// an optional class.
type Value struct {
	Class    uislot.Class
	LabelKey string
	Order    *int
	Group    string
	Icon     string
}

type valueObject struct {
	Class    uislot.Class `json:"class"`
	LabelKey string       `json:"label_key"`
	Order    *int         `json:"order"`
	Group    string       `json:"group"`
	Icon     string       `json:"icon"`
}

// UnmarshalJSON accepts the string and object forms; unknown object fields fail.
func (v *Value) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*v = Value{Class: uislot.Class(s)}
		return nil
	}
	var o valueObject
	if err := strictUnmarshal(b, &o); err != nil {
		return err
	}
	*v = Value{Class: o.Class, LabelKey: o.LabelKey, Order: o.Order, Group: o.Group, Icon: o.Icon}
	return nil
}

func (v Value) hasAmendmentFields() bool {
	return v.LabelKey != "" || v.Order != nil || v.Group != "" || v.Icon != ""
}

// LegacyEntry is one amendment of the pre-template layout a legacy shop keeps
// (E3): the old salon plugin's amendments, replayed verbatim.
type LegacyEntry struct {
	Key      string `json:"key"`
	Hide     bool   `json:"hide"`
	Order    *int   `json:"order"`
	LabelKey string `json:"label_key"`
	Group    string `json:"group"`
	Icon     string `json:"icon"`
}

// Legacy is the legacy_layout block, per slot, in declared order.
type Legacy struct {
	Menu     []LegacyEntry `json:"menu"`
	Items    []LegacyEntry `json:"items"`
	Rail     []LegacyEntry `json:"rail"`
	Settings []LegacyEntry `json:"settings"`
}

// Template is one schema-1 template file.
type Template struct {
	Schema         int              `json:"schema"`
	ID             string           `json:"id"`
	Version        int              `json:"version"`
	ShopTypes      []string         `json:"shop_types"`
	Extends        string           `json:"extends"`
	LabelKey       string           `json:"label_key"`
	SummaryKey     string           `json:"summary_key"`
	Seed           []Seed           `json:"seed"`
	Settings       map[string]Value `json:"settings"`
	SettingsLayout map[string]Value `json:"settings_layout"`
	Menu           map[string]Value `json:"menu"`
	Items          map[string]Value `json:"items"`
	Rail           map[string]Value `json:"rail"`
	Suggest        []string         `json:"suggest"`
	LegacyLayout   *Legacy          `json:"legacy_layout"`
	// IconPack and Theme are reserved (ADR-0190 amendment 4): accepted and
	// ignored in v1; no shipped template sets them.
	IconPack string `json:"icon_pack"`
	Theme    string `json:"theme"`
}

// Set is the loaded, validated, resolved template set, keyed by shop type.
type Set struct {
	byType map[string]*Resolved
}

// Resolved is one shop type's template after extends-resolution, with every
// lookup precomputed. Immutable after Load; safe for concurrent use.
type Resolved struct {
	id, labelKey, summaryKey string
	version                  int
	suggest                  []string
	classes                  map[Kind]map[string]uislot.Class
	amend                    map[string][]uislot.Amendment
	legacy                   map[string][]uislot.Amendment
	hasLegacy                bool
	seeds                    []Seed
}

// Load parses and validates the embedded templates against cat. A returned
// error means "run with no template" (the caller logs it); it never panics.
func Load(cat Catalog) (*Set, error) { return loadFS(embedded, cat) }

func loadFS(fsys fs.FS, cat Catalog) (*Set, error) {
	if cat == nil {
		return nil, fmt.Errorf("shoptemplate: a Catalog is required")
	}
	names, err := fs.Glob(fsys, "templates/*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var ts []*Template
	for _, n := range names {
		raw, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, err
		}
		var t Template
		if err := strictUnmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("shoptemplate: %s: %w", n, err)
		}
		ts = append(ts, &t)
	}
	return Build(ts, cat, true)
}

// Build validates, resolves extends and precomputes. requireAllTypes demands
// a template for every shoptype.List() entry (Load does; shoptemplatetest
// does not).
func Build(ts []*Template, cat Catalog, requireAllTypes bool) (*Set, error) {
	byID, err := validateSet(ts, cat, requireAllTypes)
	if err != nil {
		return nil, err
	}
	set := &Set{byType: map[string]*Resolved{}}
	for _, t := range ts {
		eff := effective(t, byID)
		r := precompute(eff)
		for _, st := range t.ShopTypes {
			set.byType[st] = r
		}
	}
	return set, nil
}

// ForType returns the resolved template for a shop type, or nil ("no
// template") for ""/unknown types and a nil Set. It never allocates.
func (s *Set) ForType(shopType string) *Resolved {
	if s == nil {
		return nil
	}
	return s.byType[shopType]
}

// ID is the template id ("" for nil).
func (r *Resolved) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

// Version is the template version (0 for nil).
func (r *Resolved) Version() int {
	if r == nil {
		return 0
	}
	return r.version
}

// LabelKey / SummaryKey are the en.json keys for the wizard tile.
func (r *Resolved) LabelKey() string {
	if r == nil {
		return ""
	}
	return r.labelKey
}

// SummaryKey: see LabelKey.
func (r *Resolved) SummaryKey() string {
	if r == nil {
		return ""
	}
	return r.summaryKey
}

// Suggest returns the capabilities the template suggests (read-only).
func (r *Resolved) Suggest() []string {
	if r == nil {
		return nil
	}
	return r.suggest
}

// ClassFor returns the template's class for the row, or base when the
// template says nothing about it (or r is nil). A map read, no allocation.
func (r *Resolved) ClassFor(kind Kind, key string, base uislot.Class) uislot.Class {
	if r == nil {
		return base
	}
	if c, ok := r.classes[kind][key]; ok {
		return c
	}
	return base
}

// Amendments returns the template's amendments for a slot, sorted by key,
// stamped PluginID core.template (nil for a nil receiver or a slot with
// none). Read-only: callers must not modify the slice.
func (r *Resolved) Amendments(slot string) []uislot.Amendment {
	if r == nil {
		return nil
	}
	return r.amend[slot]
}

// LegacyAmendments returns the legacy_layout amendments for a slot, in
// declared order (E3): a legacy shop keeps its old layout until it accepts
// the template.
func (r *Resolved) LegacyAmendments(slot string) []uislot.Amendment {
	if r == nil {
		return nil
	}
	return r.legacy[slot]
}

// HasLegacy reports whether the template carries a legacy_layout block.
func (r *Resolved) HasLegacy() bool { return r != nil && r.hasLegacy }

// Seeds returns a copy of the apply-once seeds (nil when none).
func (r *Resolved) Seeds() []Seed {
	if r == nil || len(r.seeds) == 0 {
		return nil
	}
	return append([]Seed(nil), r.seeds...)
}

func strictUnmarshal(b []byte, v any) error {
	dec := json.NewDecoder(bytesReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data after the template object")
	}
	return nil
}
