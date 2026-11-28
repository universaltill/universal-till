package pages

import (
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/shoptemplate"
	"github.com/universaltill/universal-till/internal/shoptemplate/shoptemplatetest"
)

// useSeedTemplates wires the template seam to the content the shipped
// templates carry for the order-type default: retail and service seed
// sale.order_type_prompt=off, cafe and hospitality seed nothing.
func useSeedTemplates(t *testing.T) {
	t.Helper()
	off := []shoptemplate.Seed{{Key: data.OrderTypePromptModeKey, Value: data.OrderTypePromptModeOff}}
	set := shoptemplatetest.TestSet(t,
		&shoptemplate.Template{Schema: 1, ID: "retail", Version: 1, ShopTypes: []string{"retail"}, Seed: off},
		&shoptemplate.Template{Schema: 1, ID: "service", Version: 1, ShopTypes: []string{"service"}, Seed: off},
		&shoptemplate.Template{Schema: 1, ID: "cafe", Version: 1, ShopTypes: []string{"cafe", "hospitality"}},
	)
	prev := shopTemplateFor
	shopTemplateFor = func(_ *common.Deps, st string) *shoptemplate.Resolved { return set.ForType(st) }
	t.Cleanup(func() { shopTemplateFor = prev; httpx.InitOrderTypePromptMode("top") })
}

func TestRetailAndServiceSeedOrderTypePromptOff(t *testing.T) {
	useSeedTemplates(t)
	for _, st := range []string{"retail", "service"} {
		_, _, d := newFullAuthDeps(t)
		httpx.InitOrderTypePromptMode("top")
		if err := saveSetupShopType(t.Context(), d, st); err != nil {
			t.Fatal(err)
		}
		if v, _, _ := d.Settings.Get(t.Context(), data.OrderTypePromptModeKey); v != data.OrderTypePromptModeOff {
			t.Fatalf("%s: %s = %q, want off", st, data.OrderTypePromptModeKey, v)
		}
		if !httpx.OrderTypeOff() {
			t.Fatalf("%s: live order-type mode not republished", st)
		}
		if v, _, _ := d.Settings.Get(t.Context(), shoptemplate.LedgerKey); v == "" {
			t.Fatalf("%s: no %s ledger", st, shoptemplate.LedgerKey)
		}
	}
}

func TestCafeAndHospitalityLeaveItUnset(t *testing.T) {
	useSeedTemplates(t)
	for _, st := range []string{"cafe", "hospitality"} {
		_, _, d := newFullAuthDeps(t)
		if err := saveSetupShopType(t.Context(), d, st); err != nil {
			t.Fatal(err)
		}
		if v, ok, _ := d.Settings.Get(t.Context(), data.OrderTypePromptModeKey); ok && v != "" {
			t.Fatalf("%s: %s = %q, want unset (default top)", st, data.OrderTypePromptModeKey, v)
		}
	}
}

func TestApplyShopTemplateKeepsExistingValueAndRunsOnce(t *testing.T) {
	useSeedTemplates(t)
	_, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), data.OrderTypePromptModeKey, data.OrderTypePromptModeAtPay); err != nil {
		t.Fatal(err)
	}
	applyShopTemplate(t.Context(), d, "retail")
	if v, _, _ := d.Settings.Get(t.Context(), data.OrderTypePromptModeKey); v != data.OrderTypePromptModeAtPay {
		t.Fatalf("restored value overwritten: %q, want at_pay", v)
	}
	// Owner clears it; a second type save must not re-seed (ledger exists).
	if err := d.Settings.Set(t.Context(), data.OrderTypePromptModeKey, ""); err != nil {
		t.Fatal(err)
	}
	applyShopTemplate(t.Context(), d, "retail")
	if v, _, _ := d.Settings.Get(t.Context(), data.OrderTypePromptModeKey); v != "" {
		t.Fatalf("second apply re-seeded %q", v)
	}
}

func TestApplyShopTemplateSatelliteWritesNothing(t *testing.T) {
	useSeedTemplates(t)
	_, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	applyShopTemplate(t.Context(), d, "retail")
	if v, _, _ := d.Settings.Get(t.Context(), data.OrderTypePromptModeKey); v != "" {
		t.Fatalf("satellite seeded %q", v)
	}
	if v, _, _ := d.Settings.Get(t.Context(), shoptemplate.LedgerKey); v != "" {
		t.Fatalf("satellite wrote a ledger %q", v)
	}
}

func TestApplyShopTemplateNoTemplateIsNoOp(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	applyShopTemplate(t.Context(), d, "retail")
	if v, _, _ := d.Settings.Get(t.Context(), shoptemplate.LedgerKey); v != "" {
		t.Fatalf("ledger %q written with no template", v)
	}
}

// A satellite seeds only its own per-till theme from the template: no
// shop-wide key, no ledger (design section 3 point 4).
func TestApplyShopTemplateSatelliteSeedsOnlyPerTillTheme(t *testing.T) {
	set := shoptemplatetest.TestSet(t, &shoptemplate.Template{
		Schema: 1, ID: "retail", Version: 1, ShopTypes: []string{"retail"},
		Seed: []shoptemplate.Seed{
			{Key: data.OrderTypePromptModeKey, Value: data.OrderTypePromptModeOff},
			{Key: "theme", Value: "dark"},
		},
	})
	prev := shopTemplateFor
	shopTemplateFor = func(_ *common.Deps, st string) *shoptemplate.Resolved { return set.ForType(st) }
	t.Cleanup(func() { shopTemplateFor = prev })

	_, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	applyShopTemplate(t.Context(), d, "retail")
	if v, _, _ := d.Settings.Get(t.Context(), "theme"); v != "dark" {
		t.Fatalf("satellite theme = %q, want dark", v)
	}
	if v, _, _ := d.Settings.Get(t.Context(), shoptemplate.ThemeSeededKey); v != "dark" {
		t.Fatalf("satellite theme marker = %q, want dark", v)
	}
	if v, _, _ := d.Settings.Get(t.Context(), data.OrderTypePromptModeKey); v != "" {
		t.Fatalf("satellite seeded shop-wide %s = %q", data.OrderTypePromptModeKey, v)
	}
	if v, _, _ := d.Settings.Get(t.Context(), shoptemplate.LedgerKey); v != "" {
		t.Fatalf("satellite wrote a ledger %q", v)
	}
}
