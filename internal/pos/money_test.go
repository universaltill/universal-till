package pos

import "testing"

func TestAmountForQuantity(t *testing.T) {
	tests := []struct {
		name      string
		unitPrice int64
		qty       float64
		want      int64
	}{
		{"integer qty", 199, 2, 398},
		{"weighed half up", 599, 0.5, 300},     // 599 * 0.5 = 299.5 -> 300
		{"tiny qty rounds down", 199, 0.1, 20}, // 19.9 -> 20
		{"rounds properly", 105, 1.3, 137},     // 136.5 -> 137
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AmountForQuantity(tt.unitPrice, tt.qty); got != tt.want {
				t.Fatalf("AmountForQuantity() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestComputeTaxBasisPoints(t *testing.T) {
	type args struct {
		subtotal int64
		rateBP   int
		incl     bool
	}
	tests := []struct {
		name  string
		args  args
		tax   int64
		total int64
	}{
		{"exclusive 20%", args{subtotal: 10000, rateBP: 2000, incl: false}, 2000, 12000},
		{"exclusive rounding", args{subtotal: 999, rateBP: 2000, incl: false}, 200, 1199},
		{"inclusive 20%", args{subtotal: 12000, rateBP: 2000, incl: true}, 2000, 12000},
		{"inclusive rounding", args{subtotal: 999, rateBP: 2000, incl: true}, 166, 999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tax, total := ComputeTaxBasisPoints(tt.args.subtotal, tt.args.rateBP, tt.args.incl)
			if tax != tt.tax || total != tt.total {
				t.Fatalf("ComputeTaxBasisPoints() = (%d,%d), want (%d,%d)", tax, total, tt.tax, tt.total)
			}
		})
	}
}
