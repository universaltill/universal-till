package reportperiod

import (
	"testing"
	"time"
)

// mustBerlin loads Europe/Berlin for the DST-transition cases. Skipping is a
// last resort for a sandbox with no tzdata — the suite must genuinely try
// the real zone first.
func mustBerlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("Europe/Berlin unavailable in this environment: %v", err)
	}
	return loc
}

func TestParseKind(t *testing.T) {
	valid := map[string]Kind{
		"day":   Day,
		"week":  Week,
		"month": Month,
		"year":  Year,
	}
	for s, want := range valid {
		got, ok := ParseKind(s)
		if !ok || got != want {
			t.Errorf("ParseKind(%q) = (%q, %v), want (%q, true)", s, got, ok, want)
		}
	}
	for _, s := range []string{"", "fortnight", "DAY", "days", "7"} {
		if got, ok := ParseKind(s); ok {
			t.Errorf("ParseKind(%q) = (%q, true), want ok=false", s, got)
		}
	}
}

func TestWindowUTC(t *testing.T) {
	utc := time.UTC
	d := func(y int, m time.Month, day, h, min int) time.Time {
		return time.Date(y, m, day, h, min, 0, 0, utc)
	}
	cases := []struct {
		name      string
		kind      Kind
		ref       time.Time
		wantStart time.Time
		wantEnd   time.Time
	}{
		{"day mid-month", Day, d(2026, time.August, 12, 10, 30), d(2026, time.August, 12, 0, 0), d(2026, time.August, 13, 0, 0)},
		{"day month-end Jan 31", Day, d(2026, time.January, 31, 23, 59), d(2026, time.January, 31, 0, 0), d(2026, time.February, 1, 0, 0)},
		{"day year-end Dec 31", Day, d(2026, time.December, 31, 12, 0), d(2026, time.December, 31, 0, 0), d(2027, time.January, 1, 0, 0)},
		{"day leap-year Feb 28", Day, d(2028, time.February, 28, 8, 0), d(2028, time.February, 28, 0, 0), d(2028, time.February, 29, 0, 0)},
		{"day leap-year Feb 29", Day, d(2028, time.February, 29, 8, 0), d(2028, time.February, 29, 0, 0), d(2028, time.March, 1, 0, 0)},

		// 2026-08-12 is a Wednesday; week is Monday 2026-08-10 .. Monday 2026-08-17.
		{"week mid-week Wednesday", Week, d(2026, time.August, 12, 10, 0), d(2026, time.August, 10, 0, 0), d(2026, time.August, 17, 0, 0)},
		// 2026-08-10 is a Monday: the week starts that same day.
		{"week on Monday", Week, d(2026, time.August, 10, 0, 0), d(2026, time.August, 10, 0, 0), d(2026, time.August, 17, 0, 0)},
		// 2026-08-16 is a Sunday: still the week that began Monday the 10th.
		{"week on Sunday", Week, d(2026, time.August, 16, 23, 0), d(2026, time.August, 10, 0, 0), d(2026, time.August, 17, 0, 0)},
		// 2026-01-01 is a Thursday; its week began Monday 2025-12-29 (crosses year end).
		{"week across year boundary", Week, d(2026, time.January, 1, 9, 0), d(2025, time.December, 29, 0, 0), d(2026, time.January, 5, 0, 0)},

		{"month mid-month", Month, d(2026, time.August, 12, 10, 0), d(2026, time.August, 1, 0, 0), d(2026, time.September, 1, 0, 0)},
		{"month Jan 31 boundary", Month, d(2026, time.January, 31, 23, 0), d(2026, time.January, 1, 0, 0), d(2026, time.February, 1, 0, 0)},
		{"month December", Month, d(2026, time.December, 31, 23, 0), d(2026, time.December, 1, 0, 0), d(2027, time.January, 1, 0, 0)},
		{"month leap February", Month, d(2028, time.February, 15, 12, 0), d(2028, time.February, 1, 0, 0), d(2028, time.March, 1, 0, 0)},

		{"year mid-year", Year, d(2026, time.August, 12, 10, 0), d(2026, time.January, 1, 0, 0), d(2027, time.January, 1, 0, 0)},
		{"year Dec 31", Year, d(2026, time.December, 31, 23, 59), d(2026, time.January, 1, 0, 0), d(2027, time.January, 1, 0, 0)},
		{"year Jan 1", Year, d(2026, time.January, 1, 0, 0), d(2026, time.January, 1, 0, 0), d(2027, time.January, 1, 0, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := Window(tc.kind, tc.ref, utc, 0)
			if !start.Equal(tc.wantStart) || !end.Equal(tc.wantEnd) {
				t.Fatalf("Window(%s, %s) = [%s, %s), want [%s, %s)",
					tc.kind, tc.ref, start, end, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

// TestWindowBusinessDayStart: with a 4h business-day start, 01:30 local is
// still the PREVIOUS business day; 04:30 belongs to the current one — for
// every kind.
func TestWindowBusinessDayStart(t *testing.T) {
	utc := time.UTC
	dayStart := 4 * time.Hour
	d := func(y int, m time.Month, day, h, min int) time.Time {
		return time.Date(y, m, day, h, min, 0, 0, utc)
	}

	// Reference: 2026-08-12 (Wednesday). 01:30 → business date 2026-08-11
	// (Tuesday); 04:30 → business date 2026-08-12.
	early := d(2026, time.August, 12, 1, 30)
	late := d(2026, time.August, 12, 4, 30)

	cases := []struct {
		name      string
		kind      Kind
		ref       time.Time
		wantStart time.Time
		wantEnd   time.Time
	}{
		{"day 01:30 is previous business day", Day, early, d(2026, time.August, 11, 4, 0), d(2026, time.August, 12, 4, 0)},
		{"day 04:30 is current business day", Day, late, d(2026, time.August, 12, 4, 0), d(2026, time.August, 13, 4, 0)},
		// Business date 2026-08-11 is a Tuesday → week starts Monday 2026-08-10 04:00.
		{"week 01:30", Week, early, d(2026, time.August, 10, 4, 0), d(2026, time.August, 17, 4, 0)},
		{"week 04:30", Week, late, d(2026, time.August, 10, 4, 0), d(2026, time.August, 17, 4, 0)},
		{"month 01:30", Month, early, d(2026, time.August, 1, 4, 0), d(2026, time.September, 1, 4, 0)},
		{"month 04:30", Month, late, d(2026, time.August, 1, 4, 0), d(2026, time.September, 1, 4, 0)},
		{"year 01:30", Year, early, d(2026, time.January, 1, 4, 0), d(2027, time.January, 1, 4, 0)},
		{"year 04:30", Year, late, d(2026, time.January, 1, 4, 0), d(2027, time.January, 1, 4, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := Window(tc.kind, tc.ref, utc, dayStart)
			if !start.Equal(tc.wantStart) || !end.Equal(tc.wantEnd) {
				t.Fatalf("Window(%s, %s, dayStart=4h) = [%s, %s), want [%s, %s)",
					tc.kind, tc.ref, start, end, tc.wantStart, tc.wantEnd)
			}
		})
	}

	// Month boundary with dayStart: 2026-09-01 01:30 local is still business
	// date 2026-08-31 → the AUGUST month window, not September's.
	t.Run("month boundary 01:30 on the 1st", func(t *testing.T) {
		start, end := Window(Month, d(2026, time.September, 1, 1, 30), utc, dayStart)
		if !start.Equal(d(2026, time.August, 1, 4, 0)) || !end.Equal(d(2026, time.September, 1, 4, 0)) {
			t.Fatalf("got [%s, %s), want August window", start, end)
		}
	})
	// Year boundary with dayStart: 2027-01-01 01:30 is still business date
	// 2026-12-31 → the 2026 year window.
	t.Run("year boundary 01:30 on Jan 1", func(t *testing.T) {
		start, end := Window(Year, d(2027, time.January, 1, 1, 30), utc, dayStart)
		if !start.Equal(d(2026, time.January, 1, 4, 0)) || !end.Equal(d(2027, time.January, 1, 4, 0)) {
			t.Fatalf("got [%s, %s), want 2026 window", start, end)
		}
	})
}

// TestWindowDSTSpringForward: Europe/Berlin 2026-03-29 (02:00→03:00): the day
// has only 23 real hours. A raw start.Add(24h) would leak into the next day.
func TestWindowDSTSpringForward(t *testing.T) {
	berlin := mustBerlin(t)
	ref := time.Date(2026, time.March, 29, 12, 0, 0, 0, berlin)
	start, end := Window(Day, ref, berlin, 0)
	wantStart := time.Date(2026, time.March, 29, 0, 0, 0, 0, berlin)
	wantEnd := time.Date(2026, time.March, 30, 0, 0, 0, 0, berlin)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("got [%s, %s), want [%s, %s)", start, end, wantStart, wantEnd)
	}
	if got := end.Sub(start); got != 23*time.Hour {
		t.Fatalf("spring-forward day duration = %s, want 23h", got)
	}
}

// TestWindowDSTFallBack: Europe/Berlin 2026-10-25 (03:00→02:00): 25 real hours.
func TestWindowDSTFallBack(t *testing.T) {
	berlin := mustBerlin(t)
	ref := time.Date(2026, time.October, 25, 12, 0, 0, 0, berlin)
	start, end := Window(Day, ref, berlin, 0)
	wantStart := time.Date(2026, time.October, 25, 0, 0, 0, 0, berlin)
	wantEnd := time.Date(2026, time.October, 26, 0, 0, 0, 0, berlin)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("got [%s, %s), want [%s, %s)", start, end, wantStart, wantEnd)
	}
	if got := end.Sub(start); got != 25*time.Hour {
		t.Fatalf("fall-back day duration = %s, want 25h", got)
	}
}

// TestWindowDSTWeekMonth: the week and month containing a DST jump still
// start/end on the correct wall-clock instants (start is built from
// time.Date, never Add).
func TestWindowDSTWeekMonth(t *testing.T) {
	berlin := mustBerlin(t)
	ref := time.Date(2026, time.March, 29, 12, 0, 0, 0, berlin) // Sunday of the spring-forward
	wStart, wEnd := Window(Week, ref, berlin, 0)
	// Monday of that week is 2026-03-23; next Monday 2026-03-30.
	if !wStart.Equal(time.Date(2026, time.March, 23, 0, 0, 0, 0, berlin)) ||
		!wEnd.Equal(time.Date(2026, time.March, 30, 0, 0, 0, 0, berlin)) {
		t.Fatalf("week over spring-forward = [%s, %s)", wStart, wEnd)
	}
	if got := wEnd.Sub(wStart); got != 7*24*time.Hour-time.Hour {
		t.Fatalf("spring-forward week duration = %s, want 167h", got)
	}
	mStart, mEnd := Window(Month, ref, berlin, 0)
	if !mStart.Equal(time.Date(2026, time.March, 1, 0, 0, 0, 0, berlin)) ||
		!mEnd.Equal(time.Date(2026, time.April, 1, 0, 0, 0, 0, berlin)) {
		t.Fatalf("month over spring-forward = [%s, %s)", mStart, mEnd)
	}
}

func TestRollingWindow(t *testing.T) {
	now := time.Date(2026, time.August, 12, 10, 30, 0, 0, time.UTC)
	for _, days := range []int{1, 7, 14, 30, 90, 365} {
		start, end := RollingWindow(days, now)
		if !end.Equal(now) {
			t.Fatalf("RollingWindow(%d) end = %s, want now", days, end)
		}
		if got, want := end.Sub(start), time.Duration(days)*24*time.Hour; got != want {
			t.Fatalf("RollingWindow(%d) span = %s, want %s", days, got, want)
		}
	}
}
