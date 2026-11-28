package shoptemplate

import (
	"bytes"
	"io"
	"sort"

	"github.com/universaltill/universal-till/internal/uislot"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// effective flattens a template with its (single-level) parent: the child
// overrides the parent per key; seeds merge by key; suggest unions; a child's
// legacy_layout replaces the parent's.
func effective(t *Template, byID map[string]*Template) *Template {
	if t.Extends == "" {
		return t
	}
	p := byID[t.Extends]
	out := *t
	out.Settings = mergeValues(p.Settings, t.Settings)
	out.SettingsLayout = mergeValues(p.SettingsLayout, t.SettingsLayout)
	out.Menu = mergeValues(p.Menu, t.Menu)
	out.Items = mergeValues(p.Items, t.Items)
	out.Rail = mergeValues(p.Rail, t.Rail)
	seen := map[string]int{}
	out.Seed = nil
	for _, s := range append(append([]Seed(nil), p.Seed...), t.Seed...) {
		if i, ok := seen[s.Key]; ok {
			out.Seed[i] = s
			continue
		}
		seen[s.Key] = len(out.Seed)
		out.Seed = append(out.Seed, s)
	}
	have := map[string]bool{}
	out.Suggest = nil
	for _, c := range append(append([]string(nil), p.Suggest...), t.Suggest...) {
		if !have[c] {
			have[c] = true
			out.Suggest = append(out.Suggest, c)
		}
	}
	if out.LegacyLayout == nil {
		out.LegacyLayout = p.LegacyLayout
	}
	return &out
}

func mergeValues(parent, child map[string]Value) map[string]Value {
	if len(parent) == 0 {
		return child
	}
	m := make(map[string]Value, len(parent)+len(child))
	for k, v := range parent {
		m[k] = v
	}
	for k, v := range child {
		m[k] = v
	}
	return m
}

func precompute(t *Template) *Resolved {
	r := &Resolved{
		id: t.ID, version: t.Version, labelKey: t.LabelKey, summaryKey: t.SummaryKey,
		suggest: append([]string(nil), t.Suggest...),
		classes: map[Kind]map[string]uislot.Class{},
		amend:   map[string][]uislot.Amendment{},
		legacy:  map[string][]uislot.Amendment{},
		seeds:   append([]Seed(nil), t.Seed...),
	}
	families := []struct {
		kind Kind
		slot string
		vals map[string]Value
		hide bool // a class of advanced hides the tile (menu only)
	}{
		{KindSettings, "", t.Settings, false},
		{KindSettings, slotSettings, t.SettingsLayout, false},
		{KindMenu, slotMenu, t.Menu, true},
		{KindItems, slotItems, t.Items, false},
		{KindRail, slotRail, t.Rail, false},
	}
	for _, f := range families {
		if len(f.vals) == 0 {
			continue
		}
		if r.classes[f.kind] == nil {
			r.classes[f.kind] = map[string]uislot.Class{}
		}
		keys := make([]string, 0, len(f.vals))
		for k := range f.vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := f.vals[k]
			if v.Class != "" {
				r.classes[f.kind][k] = v.Class
			}
			if f.slot == "" {
				continue
			}
			hide := f.hide && v.Class == uislot.ClassAdvanced
			if !hide && !v.hasAmendmentFields() {
				continue
			}
			r.amend[f.slot] = append(r.amend[f.slot], uislot.Amendment{
				PluginID: PluginID, Slot: f.slot, Key: k, Hide: hide,
				Order: v.Order, LabelKey: v.LabelKey, Group: v.Group, Icon: v.Icon,
			})
		}
	}
	if l := t.LegacyLayout; l != nil {
		r.hasLegacy = true
		for slot, es := range map[string][]LegacyEntry{slotMenu: l.Menu, slotItems: l.Items, slotRail: l.Rail, slotSettings: l.Settings} {
			for _, e := range es {
				r.legacy[slot] = append(r.legacy[slot], uislot.Amendment{
					PluginID: PluginID, Slot: slot, Key: e.Key, Hide: e.Hide,
					Order: e.Order, LabelKey: e.LabelKey, Group: e.Group, Icon: e.Icon,
				})
			}
		}
	}
	return r
}
