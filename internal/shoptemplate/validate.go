package shoptemplate

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/universaltill/universal-till/internal/shoptype"
	"github.com/universaltill/universal-till/internal/uislot"
)

var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// validateSet checks every rule of ba-A S2 and returns the templates by id.
// Errors name the template id and the offending key, so a bad content PR
// reads straight off the failure.
func validateSet(ts []*Template, cat Catalog, requireAllTypes bool) (map[string]*Template, error) {
	byID := map[string]*Template{}
	for _, t := range ts {
		if t.Schema != SchemaVersion {
			return nil, fmt.Errorf("shoptemplate: %q: schema %d, this build reads schema %d", t.ID, t.Schema, SchemaVersion)
		}
		if !idRe.MatchString(t.ID) {
			return nil, fmt.Errorf("shoptemplate: bad id %q (lower-case letters, digits, underscore)", t.ID)
		}
		if _, dup := byID[t.ID]; dup {
			return nil, fmt.Errorf("shoptemplate: id %q declared twice", t.ID)
		}
		byID[t.ID] = t
	}

	owner := map[string]string{}
	for _, t := range ts {
		if t.Version < 1 {
			return nil, fmt.Errorf("shoptemplate: %q: version must be >= 1", t.ID)
		}
		if len(t.ShopTypes) == 0 {
			return nil, fmt.Errorf("shoptemplate: %q: shop_types is empty", t.ID)
		}
		for _, st := range t.ShopTypes {
			if !shoptype.IsKnown(st) {
				return nil, fmt.Errorf("shoptemplate: %q: unknown shop type %q", t.ID, st)
			}
			if prev, ok := owner[st]; ok {
				return nil, fmt.Errorf("shoptemplate: shop type %q is claimed by more than one template (%q and %q)", st, prev, t.ID)
			}
			owner[st] = t.ID
		}
	}
	if requireAllTypes {
		for _, st := range shoptype.List() {
			if _, ok := owner[st]; !ok {
				return nil, fmt.Errorf("shoptemplate: shop type %q has no template", st)
			}
		}
	}

	for _, t := range ts {
		if err := validateExtends(t, byID); err != nil {
			return nil, err
		}
		if err := validateTemplate(t, cat); err != nil {
			return nil, err
		}
	}
	return byID, nil
}

func validateExtends(t *Template, byID map[string]*Template) error {
	if t.Extends == "" {
		return nil
	}
	if t.Extends == t.ID {
		return fmt.Errorf("shoptemplate: %q extends itself", t.ID)
	}
	p, ok := byID[t.Extends]
	if !ok {
		return fmt.Errorf("shoptemplate: %q extends unknown template %q", t.ID, t.Extends)
	}
	if p.Extends != "" {
		return fmt.Errorf("shoptemplate: %q extends %q which itself extends %q: extends is one level only (cycles and chains are rejected)", t.ID, p.ID, p.Extends)
	}
	return nil
}

func validateTemplate(t *Template, cat Catalog) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("shoptemplate: %q: "+format, append([]any{t.ID}, a...)...)
	}
	for _, k := range []string{t.LabelKey, t.SummaryKey} {
		if !cat.EnKey(k) {
			return bad("en.json has no key %q", k)
		}
	}

	rowSets := []struct {
		member string
		kind   Kind
		vals   map[string]Value
	}{
		{"settings", KindSettings, t.Settings},
		{"settings_layout", KindSettings, t.SettingsLayout},
		{"menu", KindMenu, t.Menu},
		{"items", KindItems, t.Items},
		{"rail", KindRail, t.Rail},
	}
	for _, rs := range rowSets {
		keys := sortedKeys(rs.vals)
		for _, key := range keys {
			v := rs.vals[key]
			if !cat.HasKey(rs.kind, key) {
				return bad("%s: unknown %s row %q", rs.member, rs.kind, key)
			}
			if err := validateValue(t, rs.member, rs.kind, key, v, cat); err != nil {
				return err
			}
		}
	}

	if t.LegacyLayout != nil {
		l := t.LegacyLayout
		for _, ls := range []struct {
			kind Kind
			es   []LegacyEntry
		}{{KindMenu, l.Menu}, {KindItems, l.Items}, {KindRail, l.Rail}, {KindSettings, l.Settings}} {
			for _, e := range ls.es {
				if !cat.HasKey(ls.kind, e.Key) {
					return bad("legacy_layout.%s: unknown row %q", ls.kind, e.Key)
				}
				if e.Hide && cat.FixedClass(ls.kind, e.Key) {
					return bad("legacy_layout.%s: %q is a fixed-class row and cannot be hidden", ls.kind, e.Key)
				}
				v := Value{LabelKey: e.LabelKey, Order: e.Order, Group: e.Group, Icon: e.Icon}
				if err := validateFields(t, "legacy_layout."+string(ls.kind), e.Key, v, cat); err != nil {
					return err
				}
			}
		}
	}

	for _, c := range t.Suggest {
		if !cat.CapabilityKnown(c) {
			return bad("suggest: %q is not in the manifest capability set (plugin ids and country codes are not allowed)", c)
		}
	}

	seen := map[string]bool{}
	for _, s := range t.Seed {
		if seen[s.Key] {
			return bad("seed: key %q listed twice", s.Key)
		}
		seen[s.Key] = true
		if !IsSeedable(s.Key) {
			return bad("seed: key %q is not on the closed seed whitelist", s.Key)
		}
		if !SeedValueValid(s.Key, s.Value) {
			return bad("seed: %q is not an allowed value for %q", s.Value, s.Key)
		}
		if IsPerTillSeedKey(s.Key) {
			continue
		}
		if tier, ok := cat.RegistryTier(s.Key); !ok || tier != "normal" {
			return bad("seed: key %q must be a registry row of tier normal (got %q)", s.Key, tier)
		}
	}
	return nil
}

func validateValue(t *Template, member string, kind Kind, key string, v Value, cat Catalog) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("shoptemplate: %q: "+format, append([]any{t.ID}, a...)...)
	}
	if v.Class != "" {
		if !v.Class.Valid() {
			return bad("%s: %q has invalid class %q", member, key, v.Class)
		}
		if cat.FixedClass(kind, key) {
			return bad("%s: %q is a fixed-class row (protected or statutory) and cannot be given a class", member, key)
		}
		if (kind == KindRail || kind == KindItems) && v.Class == uislot.ClassAdvanced {
			return bad("%s: %q cannot be demoted (rail and items rows are not demotable in v1)", member, key)
		}
	} else if !v.hasAmendmentFields() {
		return bad("%s: %q has neither a class nor any field", member, key)
	}
	return validateFields(t, member, key, v, cat)
}

func validateFields(t *Template, member, key string, v Value, cat Catalog) error {
	for _, k := range []string{v.LabelKey, v.Group} {
		if k != "" && !cat.EnKey(k) {
			return fmt.Errorf("shoptemplate: %q: %s: %q names en.json key %q which does not exist", t.ID, member, key, k)
		}
	}
	if v.Icon != "" && !uislot.IsKnownIconName(v.Icon) {
		return fmt.Errorf("shoptemplate: %q: %s: %q uses unknown icon %q", t.ID, member, key, v.Icon)
	}
	return nil
}

func sortedKeys(m map[string]Value) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
