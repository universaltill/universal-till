// Package reportperiod computes calendar-aligned reporting windows
// (day/week/month/year, ut-docs#519) and the legacy rolling-days window, as
// pure [start, end) instant pairs. No SQL and no DB here — the data layer
// consumes the instants; this package only does calendar arithmetic.
//
// All calendar math is built on time.Date(...) wall-clock construction and
// AddDate — never on raw 24h/168h Duration adds — so windows stay correct
// across DST transitions (a spring-forward day is 23h long, a fall-back day
// 25h; adding a fixed Duration would land on the wrong wall-clock hour).
package reportperiod

import "time"

// Kind is a calendar-aligned reporting period.
type Kind string

const (
	Day   Kind = "day"
	Week  Kind = "week"
	Month Kind = "month"
	Year  Kind = "year"
)

// ParseKind validates a period kind from a query param. ok=false means "not
// a recognized calendar period" — callers should fall back to legacy
// rolling-days mode.
func ParseKind(s string) (Kind, bool) {
	switch Kind(s) {
	case Day, Week, Month, Year:
		return Kind(s), true
	}
	return "", false
}

// Window returns the [start, end) instants of the calendar-aligned period of
// kind `kind` containing `ref`, in location `loc`, with the shop's business
// day starting `dayStart` after local midnight (e.g. 4h for a venue trading
// until 4am — a sale at 01:30 local still belongs to the PREVIOUS business
// day). Week starts Monday.
func Window(kind Kind, ref time.Time, loc *time.Location, dayStart time.Duration) (start, end time.Time) {
	h := int(dayStart / time.Hour)
	min := int(dayStart/time.Minute) % 60

	// Business date of ref: its calendar date in loc, unless ref falls
	// before that date's business-day-start instant — then it still belongs
	// to the previous business day. time.Date normalizes d-1 across
	// month/year boundaries (day 0 rolls to the previous month's last day).
	local := ref.In(loc)
	y, m, d := local.Date()
	if local.Before(time.Date(y, m, d, h, min, 0, 0, loc)) {
		y, m, d = time.Date(y, m, d-1, h, min, 0, 0, loc).Date()
	}

	switch kind {
	case Week:
		// Walk back to the most recent Monday (Go weekdays: Sunday=0).
		bd := time.Date(y, m, d, h, min, 0, 0, loc)
		back := (int(bd.Weekday()) + 6) % 7
		start = time.Date(y, m, d-back, h, min, 0, 0, loc)
		end = start.AddDate(0, 0, 7)
	case Month:
		start = time.Date(y, m, 1, h, min, 0, 0, loc)
		end = start.AddDate(0, 1, 0)
	case Year:
		start = time.Date(y, time.January, 1, h, min, 0, 0, loc)
		end = start.AddDate(1, 0, 0)
	default: // Day
		start = time.Date(y, m, d, h, min, 0, 0, loc)
		end = time.Date(y, m, d+1, h, min, 0, 0, loc)
	}
	return start, end
}

// RollingWindow preserves the EXACT legacy `?days=` semantics: "now minus
// N*24h" to "now", as a plain instant pair — so both this and Window feed
// the same downstream repo calls (one mechanism, not two parallel ones).
func RollingWindow(days int, now time.Time) (start, end time.Time) {
	return now.Add(-time.Duration(days) * 24 * time.Hour), now
}
