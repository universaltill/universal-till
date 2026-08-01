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
