package shoptemplate

import (
	"sort"

	"github.com/universaltill/universal-till/internal/uislot"
)

// PreviewItem names one menu tile or settings card a type change shows or
// tucks away. Key is the slot key (a route for the menu, a card id for
// settings); the page maps it to its label.
type PreviewItem struct {
	Kind Kind
	Key  string
}

// ValueChange is one opt-in value the new template would seed: the shop's
// current value (empty = never set) and the suggested one.
type ValueChange struct {
	Key       string
	Now       string
	Suggested string
}

// Preview is what changing shop type would do, computed with no writes
// (ADR-0190, ut-docs#6332 S7): visibility now, values only as an opt-in,
// plus the values the shop set itself and that therefore stay.
type Preview struct {
	Shown  []PreviewItem // tiles/cards that leave More settings / come back
	Hidden []PreviewItem // tiles/cards that move to More settings
	Values []ValueChange // owner-untouched seeds the new type suggests
	Kept   []string      // seed keys the shop set itself (never offered)
}

// LabelOnly reports a change with nothing to review beyond the label.
func (p Preview) LabelOnly() bool {
	return len(p.Shown) == 0 && len(p.Hidden) == 0 && len(p.Values) == 0
}

// BaseClass is the row's class before any template (the core table's
// class). nil means "common" for every row.
type BaseClass func(kind Kind, key string) uislot.Class

// Diff is pure: old and new are the templates in effect before and after
// (nil = none), ledger is what the old template recorded as seeded and
// current the shop's present value for each seed key. Only the menu and the
// settings cards change visibility (advanced = tucked into More settings);
// essential vs common is ordering only and is not a change.
func Diff(oldT, newT *Resolved, ledger Ledger, current map[string]string, base BaseClass) Preview {
	var p Preview
	for _, kind := range []Kind{KindMenu, KindSettings} {
		for _, key := range classKeys(oldT, newT, kind) {
			b := uislot.ClassCommon
			if base != nil {
				if c := base(kind, key); c != "" {
					b = c
				}
			}
			was := oldT.ClassFor(kind, key, b) == uislot.ClassAdvanced
			now := newT.ClassFor(kind, key, b) == uislot.ClassAdvanced
			switch {
			case !was && now:
				p.Hidden = append(p.Hidden, PreviewItem{kind, key})
			case was && !now:
				p.Shown = append(p.Shown, PreviewItem{kind, key})
			}
		}
	}

	suggested := map[string]string{}
	for _, s := range newT.Seeds() {
		if SeedValueValid(s.Key, s.Value) {
			suggested[s.Key] = s.Value
		}
	}
	keys := map[string]bool{}
	for k := range suggested {
		keys[k] = true
	}
	for k := range ledger.Seeded {
		if IsSeedable(k) {
			keys[k] = true
		}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		cur := current[k]
		seeded, wasSeeded := ledger.Seeded[k]
		ownerSet := cur != "" && !(wasSeeded && cur == seeded)
		want, wanted := suggested[k]
		switch {
		case wanted && cur == want:
			// already there
		case wanted && !ownerSet:
			p.Values = append(p.Values, ValueChange{Key: k, Now: cur, Suggested: want})
		case ownerSet:
			p.Kept = append(p.Kept, k)
		}
	}
	return p
}

// classKeys is the sorted union of the keys either template classes for a kind.
func classKeys(a, b *Resolved, kind Kind) []string {
	set := map[string]bool{}
	for _, r := range []*Resolved{a, b} {
		if r == nil {
			continue
		}
		for k := range r.classes[kind] {
			set[k] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
