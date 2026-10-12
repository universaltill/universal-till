package data

import "testing"

// ADR-0153 Decision 1 (ut-docs#2837): every registered core view carries
// exactly one AI data class from the closed list, or DataClassShop. A view
// with no class (or an unknown one) is a CI failure, because an ai.agent
// plugin is refused it at call time.
func TestCoreViews_EveryViewHasAKnownDataClass(t *testing.T) {
	known := map[string]bool{DataClassShop: true}
	for _, c := range AIDataClasses {
		known[c] = true
	}
	for name, v := range coreViews {
		if v.DataClass == "" {
			t.Errorf("view %s has no DataClass", name)
			continue
		}
		if !known[v.DataClass] {
			t.Errorf("view %s has unknown DataClass %q", name, v.DataClass)
		}
	}
}

func TestCoreViews_DataClassPerView(t *testing.T) {
	want := map[string]string{
		"sales.by_day.v1":       DataClassSales,
		"items.top.v1":          DataClassSales,
		"payments.breakdown.v1": DataClassSales,
		"sales.receipts.v1":     DataClassSales,
		"catalog.items.v1":      DataClassCatalog,
		"catalog.photos.v1":     DataClassCatalog,
		"stock.levels.v1":       DataClassStock,
		"users.list.v1":         DataClassStaff,
		// Activity rows carry actor_id: staff outranks till_health.
		"audit.summary.v1": DataClassStaff,
		"shop.context.v1":  DataClassShop,
		"shop.context.v2":  DataClassShop,
	}
	if len(want) != len(coreViews) {
		t.Fatalf("%d views registered, %d classified here: classify the new view", len(coreViews), len(want))
	}
	for name, class := range want {
		v, ok := LookupCoreView(name)
		if !ok || v.DataClass != class {
			t.Errorf("view %s DataClass = %q, want %q", name, v.DataClass, class)
		}
	}
}

func TestAIDataClasses_ClosedOrderedList(t *testing.T) {
	want := []string{"sales", "catalog", "stock", "customers", "staff", "till_health"}
	if len(AIDataClasses) != len(want) {
		t.Fatalf("AIDataClasses = %v, want %v", AIDataClasses, want)
	}
	for i := range want {
		if AIDataClasses[i] != want[i] {
			t.Fatalf("AIDataClasses = %v, want %v", AIDataClasses, want)
		}
	}
	if DataClassShop != "shop" {
		t.Fatalf("DataClassShop = %q", DataClassShop)
	}
	for _, c := range AIDataClasses {
		if c == DataClassShop {
			t.Fatal("shop must not be an AI data class: it has no grant")
		}
	}
}
