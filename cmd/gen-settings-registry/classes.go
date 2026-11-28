package main

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/registrydesc"
	"github.com/universaltill/universal-till/internal/shoptemplate"
	"github.com/universaltill/universal-till/internal/shoptype"
	"github.com/universaltill/universal-till/internal/uislot"
)

// shopTemplateClassesJSONPath is the committed artefact, relative to the repo
// root. ut-cloud pins a verbatim copy; my. reads "rarely needed for type T"
// from it (ut-docs#6352, ADR-0190). It carries no shop data.
const shopTemplateClassesJSONPath = "docs/shop-template-classes.json"

type typeClassesJSON struct {
	Template string `json:"template"`
	Version  int    `json:"version"`
	// RarelyNeededCards are Settings cards the template demotes to advanced
	// (never a fixed card, never a card that is advanced already).
	RarelyNeededCards []string `json:"rarely_needed_cards"`
	// RarelyNeededKeys are the registry setting keys living in those cards.
	RarelyNeededKeys []string `json:"rarely_needed_keys"`
}

// genCatalog validates the embedded templates for the generator. The en.json
// key check is the till's own content test; here only structure matters.
type genCatalog struct{ tier map[string]string }

func (c genCatalog) HasKey(k shoptemplate.Kind, key string) bool {
	var ok bool
	switch k {
	case shoptemplate.KindSettings:
		_, ok = uislot.CoreSettingsEntry(key)
	case shoptemplate.KindMenu:
		_, ok = uislot.CoreMenuEntry(key)
	case shoptemplate.KindItems:
		_, ok = uislot.CoreItemsEntry(key)
	case shoptemplate.KindRail:
		_, ok = uislot.CoreRailEntry(key)
	}
	return ok
}
func (c genCatalog) FixedClass(k shoptemplate.Kind, key string) bool {
	return uislot.FixedClassFor(string(k), key)
}
func (c genCatalog) RegistryTier(key string) (string, bool) { t, ok := c.tier[key]; return t, ok }
func (c genCatalog) CapabilityKnown(v string) bool          { return plugins.IsKnownCapability(v) }
func (c genCatalog) EnKey(string) bool                      { return true }

// shopTemplateClassesJSON renders {"schema":1,"protected_keys":[...],
// "shop_types":{type:{...}}}: sorted, two-space indent, trailing newline.
func shopTemplateClassesJSON() ([]byte, error) {
	tier := make(map[string]string, len(data.AgentSettings))
	for _, s := range data.AgentSettings {
		tier[s.Key] = s.Tier
	}
	set, err := shoptemplate.Load(genCatalog{tier: tier})
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	protected := []string{}
	for _, s := range data.AgentSettings {
		if uislot.FixedClassFor(uislot.KindSettings, registrydesc.CardForRegion(s.Region)) {
			protected = append(protected, s.Key)
		}
	}
	sort.Strings(protected)

	types := map[string]typeClassesJSON{}
	for _, st := range shoptype.List() {
		r := set.ForType(st)
		if r == nil {
			return nil, fmt.Errorf("shop type %q has no template", st)
		}
		cards := []string{}
		for card, c := range r.ExplicitClasses(shoptemplate.KindSettings) {
			base, ok := uislot.CoreSettingsEntry(card)
			if c != uislot.ClassAdvanced || !ok || base.Class == uislot.ClassAdvanced ||
				uislot.FixedClassFor(uislot.KindSettings, card) {
				continue
			}
			cards = append(cards, card)
		}
		sort.Strings(cards)
		inCard := map[string]bool{}
		for _, c := range cards {
			inCard[c] = true
		}
		keys := []string{}
		for _, s := range data.AgentSettings {
			if inCard[registrydesc.CardForRegion(s.Region)] {
				keys = append(keys, s.Key)
			}
		}
		sort.Strings(keys)
		types[st] = typeClassesJSON{Template: r.ID(), Version: r.Version(), RarelyNeededCards: cards, RarelyNeededKeys: keys}
	}
	b, err := json.MarshalIndent(struct {
		Schema        int                        `json:"schema"`
		ProtectedKeys []string                   `json:"protected_keys"`
		ShopTypes     map[string]typeClassesJSON `json:"shop_types"`
	}{1, protected, types}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
