package pos

import (
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/money"
)

// TipPresetsDefault is the preset list a shop gets until it sets its own
// (setting payments.tip_presets): 10, 15 and 20 percent.
const TipPresetsDefault = "10,15,20"

// maxTipPresets bounds the preset buttons so the tender panel never grows
// past one touch-friendly row or two.
const maxTipPresets = 5

// TipForPercentBP is the tip for a percentage (basis points, 1250 = 12.5%) of
// base, in integer minor units, rounded half up. A non-positive base or
// percentage gives no tip. Integer arithmetic only, never a float.
func TipForPercentBP(base money.Money, bp int64) money.Money {
	if !base.IsPositive() || bp <= 0 {
		return money.Zero
	}
	return base.MulDiv(bp, 10000)
}

// ParseTipPresets reads the payments.tip_presets setting: a comma-separated
// list of percentages with up to two decimals ("10,15,20", "12.5"), each
// above 0 and at most 100. Invalid entries and duplicates are dropped, the
// list is capped at five, and an empty or wholly invalid value yields
// TipPresetsDefault. The result is in basis points, in the order written.
func ParseTipPresets(raw string) []int64 {
	out := ParseTipPresetList(raw)
	if len(out) == 0 && strings.TrimSpace(raw) != TipPresetsDefault {
		return ParseTipPresetList(TipPresetsDefault)
	}
	return out
}

// ParseTipPresetList is ParseTipPresets without the default fallback: nil
// when nothing in raw is a valid percentage.
func ParseTipPresetList(raw string) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, part := range strings.Split(raw, ",") {
		bp, ok := percentToBP(strings.TrimSpace(part))
		if !ok || seen[bp] {
			continue
		}
		seen[bp] = true
		out = append(out, bp)
		if len(out) == maxTipPresets {
			break
		}
	}
	return out
}

// percentToBP converts "12.5" to 1250. Digits and at most one dot, at most
// two decimals, greater than 0 and at most 100 percent.
func percentToBP(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" || len(whole) > 3 || len(frac) > 2 || (hasDot && frac == "") {
		return 0, false
	}
	for _, r := range whole + frac {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, false
	}
	var f int64
	if frac != "" {
		f, _ = strconv.ParseInt(frac, 10, 64)
		if len(frac) == 1 {
			f *= 10
		}
	}
	bp := w*100 + f
	if bp <= 0 || bp > 10000 {
		return 0, false
	}
	return bp, true
}

// FormatTipPresets writes basis points back as the canonical setting value:
// 1000 -> "10", 1250 -> "12.5", 725 -> "7.25".
func FormatTipPresets(bps []int64) string {
	parts := make([]string, 0, len(bps))
	for _, bp := range bps {
		whole, frac := bp/100, bp%100
		s := strconv.FormatInt(whole, 10)
		if frac != 0 {
			f := strconv.FormatInt(frac+100, 10)[1:] // two digits, zero padded
			s += "." + strings.TrimRight(f, "0")
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}
