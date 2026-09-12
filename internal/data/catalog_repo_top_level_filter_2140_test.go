package data_test

import (
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2140 — a still-active child whose parent was deactivated (and so
// is missing from a ListActiveCategories result) must not be silently
// stranded with no chip and no way to be filtered into. TopLevelForFilterChips
// is the pure post-processing step that fixes this; these tests pin its
// behaviour directly, without a DB.

func TestTopLevelForFilterChips_OrphanedChildBecomesTopLevel(t *testing.T) {
	in := []data.CategoryNode{
		{ID: "cat-food", Name: "Food", ParentID: ""},
		// "cat-drinks" itself is NOT in this active-only slice — it was
		// deactivated — but "cat-hot-drinks" still points at it.
		{ID: "cat-hot-drinks", Name: "Hot Drinks", ParentID: "cat-drinks"},
	}
	out := data.TopLevelForFilterChips(in)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2: %+v", len(out), out)
	}
	if out[0].ParentID != "" {
		t.Fatalf("cat-food's ParentID changed unexpectedly: %+v", out[0])
	}
	if out[1].ID != "cat-hot-drinks" || out[1].ParentID != "" {
		t.Fatalf("expected cat-hot-drinks promoted to top-level (ParentID cleared), got: %+v", out[1])
	}
}

func TestTopLevelForFilterChips_LiveParentUnaffected(t *testing.T) {
	// The normal case (parent still active, still in the slice) must be a
	// complete no-op — a real nested child still folds into its parent's
	// chip, per ut-docs#2119's own design.
	in := []data.CategoryNode{
		{ID: "cat-drinks", Name: "Drinks", ParentID: ""},
		{ID: "cat-hot-drinks", Name: "Hot Drinks", ParentID: "cat-drinks"},
	}
	out := data.TopLevelForFilterChips(in)
	if out[1].ParentID != "cat-drinks" {
		t.Fatalf("expected cat-hot-drinks to keep its live parent, got: %+v", out[1])
	}
}

func TestTopLevelForFilterChips_DoesNotMutateInput(t *testing.T) {
	in := []data.CategoryNode{
		{ID: "cat-hot-drinks", Name: "Hot Drinks", ParentID: "cat-drinks"},
	}
	_ = data.TopLevelForFilterChips(in)
	if in[0].ParentID != "cat-drinks" {
		t.Fatalf("input slice was mutated: %+v", in[0])
	}
}

func TestTopLevelForFilterChips_EmptyInput(t *testing.T) {
	out := data.TopLevelForFilterChips(nil)
	if len(out) != 0 {
		t.Fatalf("expected empty output for nil input, got: %+v", out)
	}
}
