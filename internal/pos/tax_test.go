package pos

import "testing"

func TestPercentTaxEngineCompute(t *testing.T) {
	tests := []struct {
		name      string
		engine    PercentTaxEngine
		subtotal  int64
		wantTax   int64
		wantTotal int64
	}{
		{
			name:      "exclusive",
			engine:    PercentTaxEngine{RatePercent: 20, Inclusive: false},
			subtotal:  10000,
			wantTax:   2000,
			wantTotal: 12000,
		},
		{
			name:      "inclusive",
			engine:    PercentTaxEngine{RatePercent: 20, Inclusive: true},
			subtotal:  12000,
			wantTax:   2000,
			wantTotal: 12000,
		},
		{
			name:      "zero-rate",
			engine:    PercentTaxEngine{RatePercent: 0, Inclusive: false},
			subtotal:  10000,
			wantTax:   0,
			wantTotal: 10000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tax, total := tt.engine.Compute(tt.subtotal)
			if tax != tt.wantTax || total != tt.wantTotal {
				t.Fatalf("Compute(%d) => tax=%d total=%d, want tax=%d total=%d", tt.subtotal, tax, total, tt.wantTax, tt.wantTotal)
			}
		})
	}
}
