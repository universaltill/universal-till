package pos

import (
	"math"
	"testing"
)

// roundHalfUp rounds a positive float64 to the nearest integer with half-up rule.
func roundHalfUp(f float64) int64 {
	// Add a tiny epsilon to mitigate floating point representation errors
	eps := 1e-9
	return int64(math.Floor(f + 0.5 + eps))
}

func TestRoundingHalfUp_LineTotals(t *testing.T) {
	tests := []struct {
		name      string
		unitPrice int64 // minor units
		qty       float64
		want      int64 // expected rounded minor units
	}{
		{"unweighted_exact", 100, 1.000, 100},
		{"unweighted_below_half", 100, 1.004, 100},
		{"unweighted_half", 100, 1.005, 101},
		{"weighted_333", 1000, 0.333, 333},
		{"weighted_3333", 1000, 0.3333, 333},
		{"weighted_3335", 1000, 0.3335, 334},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := float64(tt.unitPrice) * tt.qty
			got := roundHalfUp(raw)
			if got != tt.want {
				t.Fatalf("%s: raw=%v got=%d want=%d", tt.name, raw, got, tt.want)
			}
		})
	}
}

func TestRoundingHalfUp_TaxCalculation(t *testing.T) {
	tests := []struct {
		name    string
		linePre int64 // pre-tax minor units
		taxBp   int64 // tax in basis points (e.g., 2000 = 20%)
		wantTax int64 // expected rounded tax minor units
	}{
		{"tax_exact", 100, 2000, 20},
		{"tax_fractional", 101, 2000, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// tax_raw = linePre * taxBp / 10000
			raw := (float64(tt.linePre) * float64(tt.taxBp)) / 10000.0
			got := roundHalfUp(raw)
			if got != tt.wantTax {
				t.Fatalf("%s: raw=%v got=%d want=%d", tt.name, raw, got, tt.wantTax)
			}
		})
	}
}
