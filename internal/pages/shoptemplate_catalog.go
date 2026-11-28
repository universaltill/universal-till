package pages

import (
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/shoptemplate"
	"github.com/universaltill/universal-till/internal/uislot"
)

// templateCatalog validates the embedded shop-type templates against what
// this build really ships: the uislot row tables, the settings registry,
// the manifest capability set and the English locale bundle (ADR-0190).
type templateCatalog struct {
	tier map[string]string
	has  func(key string) bool
}

func (c templateCatalog) HasKey(k shoptemplate.Kind, key string) bool {
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

func (c templateCatalog) FixedClass(k shoptemplate.Kind, key string) bool {
	return uislot.FixedClassFor(string(k), key)
}

func (c templateCatalog) RegistryTier(key string) (string, bool) {
	t, ok := c.tier[key]
	return t, ok
}

func (c templateCatalog) CapabilityKnown(v string) bool { return plugins.IsKnownCapability(v) }
func (c templateCatalog) EnKey(key string) bool         { return c.has(key) }

// loadShopTemplates parses and validates the embedded templates once at
// boot. An error means "no templates": every shop then renders as it did
// before templates existed (TemplateNone), never a broken menu.
func loadShopTemplates(i18n *config.I18n) (*shoptemplate.Set, error) {
	tier := make(map[string]string, len(data.AgentSettings))
	for _, s := range data.AgentSettings {
		tier[s.Key] = s.Tier
	}
	return shoptemplate.Load(templateCatalog{tier: tier, has: i18n.Has})
}
