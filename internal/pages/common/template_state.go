package common

import (
	"context"

	"github.com/universaltill/universal-till/internal/shoptemplate"
	"github.com/universaltill/universal-till/internal/uislot"
)

// TemplateMode is where a shop stands with its shop-type template
// (ADR-0190, ut-docs#6332).
type TemplateMode int

const (
	// TemplateNone: no shop type, an unknown one, or no template loaded.
	// The shop renders exactly as before templates existed.
	TemplateNone TemplateMode = iota
	// TemplateLegacy: a shop type is set but no template was ever applied
	// (shop.template_applied absent): a shop that predates templates. Its
	// layout does not change; the only template content it gets is the
	// template's legacy_layout block (the old service/salon layout).
	TemplateLegacy
	// TemplateActive: the template was applied (ledger present); its
	// classes and amendments are in force.
	TemplateActive
)

// TemplateState is the shop's effective template, read from shop.type and
// the apply-once ledger (settings synced from the main till, so a satellite
// computes the same state and never writes).
type TemplateState struct {
	Mode     TemplateMode
	ShopType string
	// Resolved is the type's template, set in Legacy and Active modes.
	Resolved *shoptemplate.Resolved
	// LegacyOff: Legacy mode where the manager had switched the old layout
	// plugin off; the legacy_layout stays silent until a template is accepted.
	LegacyOff bool
}

// Effective is the template whose classes and amendments apply: nil unless
// the mode is Active. Nil-safe consumers treat nil as "no template".
func (s TemplateState) Effective() *shoptemplate.Resolved {
	if s.Mode != TemplateActive {
		return nil
	}
	return s.Resolved
}

// Amendments are the template's amendments for one slot in this state: the
// full set when Active, the frozen legacy_layout when Legacy (nil for a
// type without one), nothing otherwise.
func (s TemplateState) Amendments(slot string) []uislot.Amendment {
	switch s.Mode {
	case TemplateActive:
		return s.Resolved.Amendments(slot)
	case TemplateLegacy:
		if s.LegacyOff {
			return nil
		}
		return s.Resolved.LegacyAmendments(slot)
	}
	return nil
}

// key identifies the state for change detection (the reconcile tick).
func (s TemplateState) key() string {
	if s.Mode == TemplateNone || s.Resolved == nil {
		return ""
	}
	if s.Mode == TemplateLegacy {
		if s.LegacyOff {
			return "legacy-off:" + s.Resolved.ID()
		}
		return "legacy:" + s.Resolved.ID()
	}
	return "active:" + s.Resolved.ID()
}

// TemplateState reads the shop's template state. Nil-safe: a Deps with no
// template set or no settings store is TemplateNone.
func (d *Deps) TemplateState(ctx context.Context) TemplateState {
	if d == nil || d.ShopTemplates == nil || d.Settings == nil {
		return TemplateState{}
	}
	shopType, _, err := d.Settings.Get(ctx, KeyShopType)
	if err != nil || shopType == "" {
		return TemplateState{}
	}
	r := d.ShopTemplates.ForType(shopType)
	if r == nil {
		return TemplateState{ShopType: shopType}
	}
	st := TemplateState{Mode: TemplateLegacy, ShopType: shopType, Resolved: r}
	if ledger, _, err := d.Settings.Get(ctx, shoptemplate.LedgerKey); err == nil && ledger != "" {
		st.Mode = TemplateActive
	} else if off, _, err := d.Settings.Get(ctx, shoptemplate.LegacyLayoutOffKey); err == nil && off == "1" {
		st.LegacyOff = true
	}
	return st
}

// withTemplate puts the template's amendments in front of the plugin ones
// (later amendments win in uislot.Resolve, so a plugin or the owner's
// restore beats the template). A template hide the owner restored is dropped
// (restored is the menu.restored_keys set; nil for slots with no restore).
// With no template amendments it returns plugin untouched, so the
// zero-amendment render path stays allocation-free.
func withTemplate(tmpl, plugin []uislot.Amendment, restored map[string]bool) []uislot.Amendment {
	if len(tmpl) == 0 {
		return plugin
	}
	out := make([]uislot.Amendment, 0, len(tmpl)+len(plugin))
	for _, a := range tmpl {
		if a.Hide && restored[a.Key] {
			continue
		}
		out = append(out, a)
	}
	out = append(out, plugin...)
	if len(out) == 0 {
		return nil
	}
	return out
}

// ReloadTemplate recomputes every slot's amendments after the shop type or
// the template ledger changed (no plugin reload: the plugin pool is already
// loaded).
func (d *Deps) ReloadTemplate(ctx context.Context) {
	d.PluginMu.Lock()
	defer d.PluginMu.Unlock()
	d.rebuildLayoutLocked(ctx)
}

// ReloadTemplateIfChanged is ReloadTemplate when the template state differs
// from the one the amendments were built for (a shop type or ledger that
// arrived by admin pull or a cloud directive). Reports whether it rebuilt.
func (d *Deps) ReloadTemplateIfChanged(ctx context.Context) bool {
	ts := d.TemplateState(ctx)
	d.PluginMu.RLock()
	same := d.templateKey == ts.key()
	d.PluginMu.RUnlock()
	if same {
		return false
	}
	d.ReloadTemplate(ctx)
	return true
}

// TemplateLayoutAmendments returns the template amendments in force for
// every slot (restores NOT applied), for Settings → Hidden menu tiles.
func (d *Deps) TemplateLayoutAmendments() []uislot.Amendment {
	d.PluginMu.RLock()
	defer d.PluginMu.RUnlock()
	var out []uislot.Amendment
	for _, slot := range layoutSlots {
		out = append(out, d.templateAmends[slot]...)
	}
	return out
}
