package pos

import "testing"

// ut-docs#213: the sale screen shows a total-quantity badge. Unit lines
// contribute their quantity; weighed lines (0.35 kg of cheese) read as one
// item each, not a fractional count.
func TestBasketItemCount(t *testing.T) {
	cases := []struct {
		name  string
		lines []BasketLine
		want  int
	}{
		{"empty", nil, 0},
		{"single unit line", []BasketLine{{Qty: 1}}, 1},
		{"unit quantities sum", []BasketLine{{Qty: 2}, {Qty: 3}}, 5},
		{"weighed line counts as one", []BasketLine{{Qty: 0.35, IsWeighed: true}}, 1},
		{"mixed", []BasketLine{{Qty: 2}, {Qty: 1.25, IsWeighed: true}, {Qty: 1}}, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := Basket{Lines: tc.lines}
			if got := b.ItemCount(); got != tc.want {
				t.Fatalf("ItemCount() = %d, want %d", got, tc.want)
			}
		})
	}
}

// ut-docs#3241: phone item tiles show how many of THAT item are in the sale.
func TestBasketItemCountsByItem(t *testing.T) {
	cases := []struct {
		name  string
		lines []BasketLine
		want  map[string]int
	}{
		{"empty", nil, map[string]int{}},
		{"one unit line qty 2", []BasketLine{{ItemID: "A", Qty: 2}}, map[string]int{"A": 2}},
		{"same item two lines summed", []BasketLine{{ItemID: "A", Qty: 1, LineKey: "1"}, {ItemID: "A", Qty: 2, NoMerge: true, LineKey: "2"}}, map[string]int{"A": 3}},
		{"two variant lines one item", []BasketLine{{ItemID: "A", VariantID: "v1", Qty: 1}, {ItemID: "A", VariantID: "v2", Qty: 1}}, map[string]int{"A": 2}},
		{"weighed counts one", []BasketLine{{ItemID: "A", Qty: 0.35, IsWeighed: true}}, map[string]int{"A": 1}},
		{"no item id skipped", []BasketLine{{ItemID: "", Qty: 3}, {ItemID: "B", Qty: 1}}, map[string]int{"B": 1}},
		{"netting to zero absent", []BasketLine{{ItemID: "A", Qty: 1}, {ItemID: "A", Qty: -2}, {ItemID: "B", Qty: 1}}, map[string]int{"B": 1}},
		{"qty 120", []BasketLine{{ItemID: "A", Qty: 120}}, map[string]int{"A": 120}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Basket{Lines: tc.lines}.ItemCountsByItem()
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}

	// Invariant: with every line item-backed and positive, tile badges sum to ItemCount().
	b := Basket{Lines: []BasketLine{{ItemID: "A", Qty: 2}, {ItemID: "B", Qty: 1.25, IsWeighed: true}, {ItemID: "A", Qty: 1}}}
	sum := 0
	for _, v := range b.ItemCountsByItem() {
		sum += v
	}
	if sum != b.ItemCount() {
		t.Fatalf("sum %d != ItemCount %d", sum, b.ItemCount())
	}
}

func TestBasketItemCountsJSON(t *testing.T) {
	if got := (Basket{}).ItemCountsJSON(); got != "{}" {
		t.Fatalf("empty = %q", got)
	}
	b := Basket{Lines: []BasketLine{{ItemID: "B", Qty: 1}, {ItemID: "A", Qty: 2}}}
	if got, want := b.ItemCountsJSON(), `{"A":2,"B":1}`; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
