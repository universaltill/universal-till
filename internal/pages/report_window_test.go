package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ut-docs#519: calendar-aligned day/week/month/year report periods,
// anchored on a configurable per-till business-day boundary instead of
// naive midnight. These are pure-function tests (no DB) against
// parseReportWindow/businessDateFor/parseBusinessDayStart in
// reports_page.go.

func reqWithQuery(t *testing.T, query string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, "/reports"+query, nil)
}

func TestParseBusinessDayStart_DefaultsAndValidates(t *testing.T) {
	cases := []struct {
		in    string
		wantH int
		wantM int
	}{
		{"", 0, 0},        // never configured
		{"garbage", 0, 0}, // malformed falls back to midnight
		{"24:00", 0, 0},   // out of range falls back
		{"06:00", 6, 0},
		{"23:59", 23, 59},
	}
	for _, c := range cases {
		h, m := parseBusinessDayStart(c.in)
		if h != c.wantH || m != c.wantM {
			t.Errorf("parseBusinessDayStart(%q) = (%d,%d), want (%d,%d)", c.in, h, m, c.wantH, c.wantM)
		}
	}
}

func TestBusinessDateFor_LateBoundaryKeepsPastMidnightSalesOnPreviousDay(t *testing.T) {
	// A bar with a 06:00 boundary: 02:00 Tuesday is still Monday's business
	// day; 08:00 Tuesday is Tuesday's.
	tuesday2am := time.Date(2026, 8, 11, 2, 0, 0, 0, time.Local) // a Tuesday
	got := businessDateFor(tuesday2am, 6, 0)
	if got.Day() != 10 || got.Month() != time.August {
		t.Fatalf("02:00 with a 06:00 boundary should still be Aug 10 (Monday), got %v", got)
	}

	tuesday8am := time.Date(2026, 8, 11, 8, 0, 0, 0, time.Local)
	got = businessDateFor(tuesday8am, 6, 0)
	if got.Day() != 11 {
		t.Fatalf("08:00 with a 06:00 boundary should be Aug 11 (Tuesday) itself, got %v", got)
	}

	// Default midnight boundary: every instant belongs to its own calendar date.
	got = businessDateFor(tuesday2am, 0, 0)
	if got.Day() != 11 {
		t.Fatalf("02:00 with the default midnight boundary should stay Aug 11, got %v", got)
	}
}

func TestParseReportWindow_MissingOrInvalidPeriodFallsBackToRollingDays(t *testing.T) {
	// No ?period at all: today's exact rolling-days behavior, unchanged.
	w := parseReportWindow(reqWithQuery(t, ""), "")
	gotDays := w.To.Sub(w.From).Hours() / 24
	if gotDays < 13.9 || gotDays > 14.1 {
		t.Fatalf("no period: window span = %.2f days, want ~14 (the default)", gotDays)
	}
	if w.Label != "" {
		t.Fatalf("rolling-days fallback must not carry a period label, got %q", w.Label)
	}

	// An invalid ?period value falls back the same way, honoring ?days=.
	w = parseReportWindow(reqWithQuery(t, "?period=fortnight&days=30"), "")
	gotDays = w.To.Sub(w.From).Hours() / 24
	if gotDays < 29.9 || gotDays > 30.1 {
		t.Fatalf("invalid period: window span = %.2f days, want ~30 (?days=30)", gotDays)
	}
}

func TestParseReportWindow_DayWeekMonthYear_AnchoredAtMidnightByDefault(t *testing.T) {
	// anchor=2026-07-15 is a Wednesday.
	day := parseReportWindow(reqWithQuery(t, "?period=day&anchor=2026-07-15"), "")
	wantFrom := time.Date(2026, 7, 15, 0, 0, 0, 0, time.Local)
	wantTo := time.Date(2026, 7, 16, 0, 0, 0, 0, time.Local)
	if !day.From.Equal(wantFrom) || !day.To.Equal(wantTo) {
		t.Fatalf("day period = [%v, %v), want [%v, %v)", day.From, day.To, wantFrom, wantTo)
	}
	if day.Label != "reports.period.day" {
		t.Fatalf("day label = %q, want reports.period.day", day.Label)
	}

	// ISO week: Monday 2026-07-13 through (exclusive) Monday 2026-07-20.
	week := parseReportWindow(reqWithQuery(t, "?period=week&anchor=2026-07-15"), "")
	wantFrom = time.Date(2026, 7, 13, 0, 0, 0, 0, time.Local)
	wantTo = time.Date(2026, 7, 20, 0, 0, 0, 0, time.Local)
	if !week.From.Equal(wantFrom) || !week.To.Equal(wantTo) {
		t.Fatalf("week period = [%v, %v), want [%v, %v)", week.From, week.To, wantFrom, wantTo)
	}

	// Calendar month: July 2026, regardless of which day in July was anchored.
	month := parseReportWindow(reqWithQuery(t, "?period=month&anchor=2026-07-15"), "")
	wantFrom = time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local)
	wantTo = time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local)
	if !month.From.Equal(wantFrom) || !month.To.Equal(wantTo) {
		t.Fatalf("month period = [%v, %v), want [%v, %v)", month.From, month.To, wantFrom, wantTo)
	}

	// Calendar year: 2026.
	year := parseReportWindow(reqWithQuery(t, "?period=year&anchor=2026-07-15"), "")
	wantFrom = time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	wantTo = time.Date(2027, 1, 1, 0, 0, 0, 0, time.Local)
	if !year.From.Equal(wantFrom) || !year.To.Equal(wantTo) {
		t.Fatalf("year period = [%v, %v), want [%v, %v)", year.From, year.To, wantFrom, wantTo)
	}
}

func TestParseReportWindow_BusinessDayStartShiftsAllPeriodBoundaries(t *testing.T) {
	// A 06:00 boundary: "day" for anchor=2026-07-15 spans 06:00 that day
	// through 06:00 the next day, not midnight to midnight.
	day := parseReportWindow(reqWithQuery(t, "?period=day&anchor=2026-07-15"), "06:00")
	wantFrom := time.Date(2026, 7, 15, 6, 0, 0, 0, time.Local)
	wantTo := time.Date(2026, 7, 16, 6, 0, 0, 0, time.Local)
	if !day.From.Equal(wantFrom) || !day.To.Equal(wantTo) {
		t.Fatalf("day period w/ 06:00 boundary = [%v, %v), want [%v, %v)", day.From, day.To, wantFrom, wantTo)
	}

	// "month" also crosses over at 06:00, not midnight.
	month := parseReportWindow(reqWithQuery(t, "?period=month&anchor=2026-07-15"), "06:00")
	wantFrom = time.Date(2026, 7, 1, 6, 0, 0, 0, time.Local)
	wantTo = time.Date(2026, 8, 1, 6, 0, 0, 0, time.Local)
	if !month.From.Equal(wantFrom) || !month.To.Equal(wantTo) {
		t.Fatalf("month period w/ 06:00 boundary = [%v, %v), want [%v, %v)", month.From, month.To, wantFrom, wantTo)
	}

	// An invalid business_day_start setting (e.g. corrupted/never-migrated
	// value) degrades to the safe midnight default rather than erroring.
	day2 := parseReportWindow(reqWithQuery(t, "?period=day&anchor=2026-07-15"), "garbage")
	wantFrom = time.Date(2026, 7, 15, 0, 0, 0, 0, time.Local)
	if !day2.From.Equal(wantFrom) {
		t.Fatalf("invalid business_day_start should default to midnight, got %v", day2.From)
	}
}

func TestParseReportWindow_InvalidAnchorFallsBackToNow(t *testing.T) {
	// A malformed anchor must not error or panic — it degrades to "now" the
	// same way an absent anchor does.
	w := parseReportWindow(reqWithQuery(t, "?period=day&anchor=not-a-date"), "")
	now := time.Now()
	if w.To.Sub(now) > 25*time.Hour || now.Sub(w.From) > 25*time.Hour {
		t.Fatalf("malformed anchor should resolve to today's window, got [%v, %v) vs now=%v", w.From, w.To, now)
	}
}

func TestParseReportWindow_NoAnchorUsesBusinessDayForNow(t *testing.T) {
	// With no anchor at all, period=day must resolve to a window that
	// actually contains "now".
	w := parseReportWindow(reqWithQuery(t, "?period=day"), "00:00")
	now := time.Now()
	if now.Before(w.From) || !now.Before(w.To) {
		t.Fatalf("period=day with no anchor must contain now=%v, got window [%v, %v)", now, w.From, w.To)
	}
}
