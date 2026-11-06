package httpx

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// byteUnits are the common.size.<unit> keys, smallest first.
var byteUnits = []string{"b", "kb", "mb", "gb", "tb"}

// FormatBytes renders n with 1024-based units: "0 B", "512 B", "1.5 KB",
// "12.0 MB", "3.2 GB", "1.1 TB" — one decimal from KB up, the number through
// FormatDecimal (locale separator + digit shape), the unit via the locale's
// "common.size.<unit>" ("%s KB"), so a language can translate or reorder it
// (ut-docs#3122). A negative n renders as 0 B.
func FormatBytes(n int64, locale string) string {
	if n < 0 {
		n = 0
	}
	unit := 0
	num := strconv.FormatInt(n, 10)
	if n >= 1024 {
		v := float64(n)
		for unit < len(byteUnits)-1 && v >= 1024 {
			v /= 1024
			unit++
		}
		r := math.Round(v*10) / 10
		// 1023.97 KB rounds to 1024.0: show 1.0 MB instead.
		if r >= 1024 && unit < len(byteUnits)-1 {
			r /= 1024
			unit++
		}
		num = strings.TrimSpace(fmt.Sprintf("%.1f", r))
	}
	return fmt.Sprintf(T(locale, "common.size."+byteUnits[unit]), FormatDecimal(num, locale))
}
