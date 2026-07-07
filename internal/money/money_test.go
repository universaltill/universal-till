package money

import (
	"encoding/json"
	"testing"
)

func TestArithmetic(t *testing.T) {
	if got := FromMinor(1000).Add(FromMinor(250)); got != 1250 {
		t.Fatalf("Add = %d", got)
	}
	if got := FromMinor(1000).Sub(FromMinor(250)); got != 750 {
		t.Fatalf("Sub = %d", got)
	}
	if got := FromMinor(500).Neg(); got != -500 {
		t.Fatalf("Neg = %d", got)
	}
	if got := FromMinor(199).Scale(3); got != 597 {
		t.Fatalf("Scale = %d", got)
	}
	if got := Sum(FromMinor(1), FromMinor(2), FromMinor(3)); got != 6 {
		t.Fatalf("Sum = %d", got)
	}
}

func TestMulQtyHalfAwayFromZero(t *testing.T) {
	// matches math.Round semantics used by the previous AmountForQuantity
	cases := []struct {
		price Money
		qty   float64
		want  Money
	}{
		{100, 2, 200},
		{199, 1.5, 299},   // 298.5 -> 299
		{333, 0.5, 167},   // 166.5 -> 167
		{100, 2.345, 235}, // 234.5 -> 235
	}
	for _, c := range cases {
		if got := c.price.MulQty(c.qty); got != c.want {
			t.Errorf("%d.MulQty(%v) = %d, want %d", c.price, c.qty, got, c.want)
		}
	}
}

func TestMulDivHalfUp(t *testing.T) {
	// exclusive tax at 2000bp (20%): 1000 * 2000 / 10000 = 200
	if got := FromMinor(1000).MulDiv(2000, 10000); got != 200 {
		t.Fatalf("MulDiv = %d", got)
	}
	if got := FromMinor(0).MulDiv(2000, 10000); got != 0 {
		t.Fatalf("MulDiv zero = %d", got)
	}
	if got := FromMinor(100).MulDiv(1, 0); got != 0 {
		t.Fatalf("MulDiv div-by-zero = %d", got)
	}
}

func TestPredicatesAndFormat(t *testing.T) {
	if !FromMinor(0).IsZero() || FromMinor(1).IsZero() {
		t.Fatal("IsZero")
	}
	if !FromMinor(1).IsPositive() || !FromMinor(-1).IsNegative() {
		t.Fatal("sign predicates")
	}
	for _, c := range []struct {
		m    Money
		want string
	}{{1234, "12.34"}, {5, "0.05"}, {100, "1.00"}, {-1234, "-12.34"}, {0, "0.00"}} {
		if got := c.m.Format(); got != c.want {
			t.Errorf("Format(%d) = %q, want %q", c.m, got, c.want)
		}
	}
}

func TestJSONIsNumeric(t *testing.T) {
	// wire-compatible with the previous int64 fields
	b, err := json.Marshal(struct {
		Total Money `json:"total"`
	}{Total: 1599})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"total":1599}` {
		t.Fatalf("marshal = %s", b)
	}
	var out struct {
		Total Money `json:"total"`
	}
	if err := json.Unmarshal([]byte(`{"total":1599}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 1599 {
		t.Fatalf("unmarshal = %d", out.Total)
	}
}

func TestScanValue(t *testing.T) {
	var m Money
	for _, src := range []any{int64(1500), int(1500), []byte("1500"), "1500"} {
		m = 0
		if err := m.Scan(src); err != nil {
			t.Fatalf("Scan(%T): %v", src, err)
		}
		if m != 1500 {
			t.Fatalf("Scan(%T) = %d", src, m)
		}
	}
	m = 0
	if err := m.Scan(nil); err != nil || m != 0 {
		t.Fatalf("Scan(nil) = %d, %v", m, err)
	}
	if err := (&m).Scan(1.5); err == nil {
		t.Fatal("Scan(float) should error")
	}
	v, err := Money(1500).Value()
	if err != nil || v.(int64) != 1500 {
		t.Fatalf("Value = %v, %v", v, err)
	}
}
