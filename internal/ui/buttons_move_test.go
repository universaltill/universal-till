package ui

import "testing"

// TestSameCategoryNeighborIndex is the one unit test for the neighbour
// helper ut-docs#2285's card asks for: sameCategoryNeighborIndex is the
// SINGLE definition of "the nearest same-category button in a direction"
// shared by ButtonStore.Move (which relocates the moving button next to
// whatever this returns) and BuildTileSheetView (which uses it only to
// decide HasPrev/HasNext) — so the sheet's Move-earlier/later buttons can
// never be enabled for a move Move itself would actually refuse, or
// disabled for one it would accept. Written before ButtonStore.Move existed
// (TDD) against a small in-memory Button slice — no DB needed for this
// walk, it's a pure function over already-loaded buttons in display order.
func TestSameCategoryNeighborIndex(t *testing.T) {
	// A(cat1) B(cat2) C(cat1) D(cat2) — the shape ut-docs#2285's own
	// TestButtonsMove_WithinSameCategoryOnly (internal/pages) exercises
	// end to end through the real HTTP handler; this pins the pure walk
	// underneath it in isolation.
	btns := []Button{
		{Code: "A", CategoryID: "cat1"},
		{Code: "B", CategoryID: "cat2"},
		{Code: "C", CategoryID: "cat1"},
		{Code: "D", CategoryID: "cat2"},
	}

	cases := []struct {
		name string
		idx  int
		dir  int
		want int // -1 = no neighbour (edge)
	}{
		{"A later skips B(cat2), lands on C(cat1)", 0, 1, 2},
		{"A earlier: nothing before it at all", 0, -1, -1},
		{"C earlier skips B(cat2), lands on A(cat1)", 2, -1, 0},
		{"C later: no cat1 button after it", 2, 1, -1},
		{"B later skips C(cat1), lands on D(cat2)", 1, 1, 3},
		{"D earlier skips C(cat1), lands on B(cat2)", 3, -1, 1},
		{"D later: nothing after it at all", 3, 1, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sameCategoryNeighborIndex(btns, tc.idx, tc.dir)
			if got != tc.want {
				t.Fatalf("sameCategoryNeighborIndex(idx=%d, dir=%d) = %d, want %d", tc.idx, tc.dir, got, tc.want)
			}
		})
	}
}

// The uncategorized bucket (CategoryID == "") is itself a "category" for
// this purpose — two uncategorized buttons are each other's neighbours,
// same as buttons.html's own synthetic bucket groups them together on the
// sale screen.
func TestSameCategoryNeighborIndex_UncategorizedShareAnEmptyCategory(t *testing.T) {
	btns := []Button{
		{Code: "X", CategoryID: ""},
		{Code: "Y", CategoryID: "cat1"},
		{Code: "Z", CategoryID: ""},
	}
	if got := sameCategoryNeighborIndex(btns, 0, 1); got != 2 {
		t.Fatalf("X later = %d, want 2 (Z, the other uncategorized button)", got)
	}
}
