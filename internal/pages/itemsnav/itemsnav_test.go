package itemsnav

import (
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/uislot"
)

func initRealI18n(t *testing.T) {
	t.Helper()
	i18n, err := config.NewI18n(filepath.Join("..", "..", "..", "web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
}

// The zero-plugin path (ut-docs#1911 AC "zero-plugin till renders an
// identical section list"): with no amendments, Resolve returns exactly
// uislot.CoreItems' five rows, in its declared order, untouched — the same
// content items_page.go's old hardcoded Sections slice always declared.
func TestResolve_ZeroAmendmentsMatchesCoreItemsExactly(t *testing.T) {
	initRealI18n(t)
	got := Resolve("en", nil)
	if len(got) != len(uislot.CoreItems) {
		t.Fatalf("want %d sections, got %d: %+v", len(uislot.CoreItems), len(got), got)
	}
	for i, e := range uislot.CoreItems {
		want := Section{NameKey: e.LabelKey, SubtitleKey: e.SubtitleKey, Href: e.Href}
		if got[i] != want {
			t.Errorf("section %d: want %+v, got %+v", i, want, got[i])
		}
	}
}

// A `layout` plugin's Items-slot amendment (the mechanism plugins/layout-
// salon demonstrates) re-labels and reorders a row — Resolve must apply it
// exactly as menu_page.go's Menu-slot path does.
func TestResolve_RelabelAndReorderAmendment(t *testing.T) {
	initRealI18n(t)
	one := 1
	got := Resolve("en", []uislot.Amendment{
		{PluginID: "com.example.layout", Slot: uislot.ItemsSlot, Key: "/catalog", LabelKey: "layout.salon.services", Order: &one},
	})
	if len(got) != len(uislot.CoreItems) {
		t.Fatalf("a relabel/reorder must not drop or add rows, got %d", len(got))
	}
	if got[0].Href != "/catalog" {
		t.Fatalf("the reordered row must now be first, got %+v", got[0])
	}
	// "layout.salon.services" has no English translation in core's own
	// locales (it ships only in plugins/layout-salon/locales) — Decision G's
	// fallback must degrade to the CORE label key, not the raw plugin key.
	if got[0].NameKey != "nav.catalog" {
		t.Fatalf("missing translation must fall back to the core label key, got %q", got[0].NameKey)
	}
	// Subtitle is never amendable — always the core declaration's value.
	if got[0].SubtitleKey != "items.library.subtitle" {
		t.Fatalf("subtitle must be untouched by a label amendment, got %q", got[0].SubtitleKey)
	}
}

// A Menu-slot amendment sharing a key string with an Items-slot core entry
// must never leak into the Items rail — the caller (items_page.go /
// itemsnav.WriteRailOOB's callers) is responsible for filtering
// pm.LayoutAmendments to ItemsAmendmentsSnapshot before calling Resolve,
// but Resolve itself is also safe: uislot.Resolve matches by Key against
// the entries passed in, and CoreItems' keys are its own distinct set.
func TestResolve_UnrelatedAmendmentKeyIsANoOp(t *testing.T) {
	initRealI18n(t)
	got := Resolve("en", []uislot.Amendment{
		{PluginID: "p", Slot: uislot.MenuSlot, Key: "/tables", Hide: true},
	})
	if len(got) != len(uislot.CoreItems) {
		t.Fatalf("an amendment naming no Items-slot key must change nothing, got %d rows", len(got))
	}
}
