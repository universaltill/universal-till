package pages

import (
	"strings"
	"testing"
	"time"
)

// ut-docs#1976: the /reports header's Today / Yesterday / This week /
// This month chip row. Each chip is a plain link that sets the existing
// ?period=&anchor= contract, and a chip is active exactly when its
// resolved window equals the window the page is currently showing — so
// these tests go through parseReportWindow rather than re-deriving dates.

func presetByKey(t *testing.T, presets []reportPreset, key string) reportPreset {
	t.Helper()
	for _, p := range presets {
		if p.Label == key {
			return p
		}
	}
	t.Fatalf("no preset with label %q in %+v", key, presets)
	return reportPreset{}
}

func activePresetLabels(presets []reportPreset) []string {
	var out []string
	for _, p := range presets {
		if p.Active {
			out = append(out, p.Label)
		}
	}
	return out
}

func TestReportPresets_ActiveChipMatchesCurrentWindow(t *testing.T) {
	today := businessDateFor(reportNow(), 0, 0)
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format("2006-01-02") }

	// A day in the current week other than today (so "This week" is proven
	// to match by window, not by anchor string).
	otherDayThisWeek := day(1)
	if wd := today.Weekday(); wd == time.Sunday {
		otherDayThisWeek = day(-1)
	}

	firstOfMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.Local).Format("2006-01-02")
	var firstOfMonthWant []string
	switch today.Day() {
	case 1:
		firstOfMonthWant = []string{"reports.preset.today"}
	case 2:
		firstOfMonthWant = []string{"reports.preset.yesterday"}
	}

	cases := []struct {
		query string
		want  []string // active preset label keys; empty = Custom
	}{
		{"", nil},             // default rolling 14 days → Custom
		{"?days=1", nil},      // rolling 24h is not the calendar day
		{"?period=year", nil}, // year lives under Custom only
		{"?period=day&anchor=" + day(-2), nil},
		{"?period=day", []string{"reports.preset.today"}},
		{"?period=day&anchor=" + day(0), []string{"reports.preset.today"}},
		{"?period=day&anchor=" + day(-1), []string{"reports.preset.yesterday"}},
		{"?period=week&anchor=" + day(0), []string{"reports.preset.this_week"}},
		{"?period=week&anchor=" + otherDayThisWeek, []string{"reports.preset.this_week"}},
		{"?period=week&anchor=" + day(-7), nil},
		{"?period=month&anchor=" + day(0), []string{"reports.preset.this_month"}},
		{"?period=month&anchor=" + today.AddDate(0, 0, -today.Day()).Format("2006-01-02"), nil},
		// Same From as "This month" but a one-day window: must not light
		// the month chip (only Today on the 1st, Yesterday on the 2nd).
		{"?period=day&anchor=" + firstOfMonth, firstOfMonthWant},
	}
	for _, c := range cases {
		r := reqWithQuery(t, c.query)
		presets := reportPresets(r, parseReportWindow(r, ""), "")
		got := activePresetLabels(presets)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%q: active presets = %v, want %v", c.query, got, c.want)
		}
	}
}

func TestReportPresets_QueriesUseBusinessDate(t *testing.T) {
	// With a business-day boundary, "today" is the business date, not the
	// calendar date — the chips must anchor on the same date the page's
	// own default window resolves to. A 23:59 boundary puts every instant
	// before it on the previous business date, so the check discriminates
	// whatever the wall-clock time the test runs at.
	if n := time.Now(); n.Hour() == 23 && n.Minute() == 59 {
		t.Skip("needs to run before 23:59 local time")
	}
	const bizStart = "23:59"
	h, m := parseBusinessDayStart(bizStart)
	today := businessDateFor(reportNow(), h, m)
	// Both sources of "today": the page's own default window (no anchor)
	// and the clock (an explicit anchor elsewhere).
	for _, q := range []string{"", "?period=year&anchor=2001-06-15"} {
		r := reqWithQuery(t, q)
		checkPresetQueries(t, reportPresets(r, parseReportWindow(r, bizStart), bizStart), today)
	}
}

func checkPresetQueries(t *testing.T, presets []reportPreset, today time.Time) {
	t.Helper()

	want := map[string]string{
		"reports.preset.today":      "period=day&anchor=" + today.Format("2006-01-02"),
		"reports.preset.yesterday":  "period=day&anchor=" + today.AddDate(0, 0, -1).Format("2006-01-02"),
		"reports.preset.this_week":  "period=week&anchor=" + today.Format("2006-01-02"),
		"reports.preset.this_month": "period=month&anchor=" + today.Format("2006-01-02"),
	}
	if len(presets) != len(want) {
		t.Fatalf("got %d presets, want %d: %+v", len(presets), len(want), presets)
	}
	for key, q := range want {
		p := presetByKey(t, presets, key)
		if got := "period=" + p.Period + "&anchor=" + p.Anchor; got != q {
			t.Errorf("%s query = %q, want %q", key, got, q)
		}
	}
}
