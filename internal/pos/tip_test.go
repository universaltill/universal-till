package pos

import (
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// A percent tip is integer minor units, half-up, never a float.
func TestTipForPercentBP(t *testing.T) {
	for _, tc := range []struct {
		name  string
		total int64
		bp    int64
		want  int64
	}{
		{"10 percent of 12.00", 1200, 1000, 120},
		{"15 percent of 12.00", 1200, 1500, 180},
		{"12.5 percent of 0.99 rounds down (12.375 -> 12)", 99, 1250, 12},
		{"12.5 percent of 1.01 rounds up (12.625 -> 13)", 101, 1250, 13},
		{"12.5 percent of 0.04 is a half, rounds up (0.5 -> 1)", 4, 1250, 1},
		{"10 percent of 0.05 is a half, rounds up (0.5 -> 1)", 5, 1000, 1},
		{"20 percent of 3.33 (66.6 -> 67)", 333, 2000, 67},
		{"zero percent is zero", 1200, 0, 0},
		{"zero total is zero", 0, 1500, 0},
		{"negative total is zero", -500, 1500, 0},
		{"negative percent is zero", 1200, -100, 0},
		{"100 percent is the whole total", 1200, 10000, 1200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TipForPercentBP(money.FromMinor(tc.total), tc.bp).Minor(); got != tc.want {
				t.Fatalf("TipForPercentBP(%d, %d bp) = %d, want %d", tc.total, tc.bp, got, tc.want)
			}
		})
	}
}

func TestParseTipPresets(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []int64
	}{
		{"default list", "10,15,20", []int64{1000, 1500, 2000}},
		{"spaces and a decimal", " 10 , 12.5 ,20", []int64{1000, 1250, 2000}},
		{"two decimals", "7.25", []int64{725}},
		{"comma as the decimal sign is a list separator, so 12,5 is two presets", "12,5", []int64{1200, 500}},
		{"empty gives the defaults", "", []int64{1000, 1500, 2000}},
		{"garbage only gives the defaults", "abc,,-5,0,101", []int64{1000, 1500, 2000}},
		{"garbage entries are dropped, good ones kept", "x,15", []int64{1500}},
		{"duplicates dropped, order kept", "15,10,15", []int64{1500, 1000}},
		{"at most five presets", "1,2,3,4,5,6,7", []int64{100, 200, 300, 400, 500}},
		{"three decimals refused", "12.345,10", []int64{1000}},
		{"100 percent allowed", "100", []int64{10000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTipPresets(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseTipPresets(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatTipPresets(t *testing.T) {
	if got := FormatTipPresets([]int64{1000, 1250, 725, 1500, 10000}); got != "10,12.5,7.25,15,100" {
		t.Fatalf("FormatTipPresets = %q", got)
	}
	if got := FormatTipPresets(nil); got != "" {
		t.Fatalf("FormatTipPresets(nil) = %q", got)
	}
}
