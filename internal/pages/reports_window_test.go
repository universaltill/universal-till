package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Calendar-aligned report periods (ut-docs#519): ?period=day|week|month|year
// selects a calendar window; anything else falls back to the legacy ?days=
// rolling window.

func TestReportsPage_PeriodParamSelectsCalendarWindow(t *testing.T) {
	mux, _ := newReportsPageTestDeps(t)

	// ?period=day: the period select shows day selected, and every tab
	// fragment URL propagates the period so tabs inherit the same window.
	rec := getReportsPage(t, mux, "?period=day")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="day" selected`) {
		t.Fatalf("expected the day period selected, got: %s", body)
	}
	if !strings.Contains(body, "period=day") {
		t.Fatalf("expected tab fragment URLs to carry period=day, got: %s", body)
	}

	// An unrecognized period falls back to rolling-days mode: the days
	// select keeps its default and no calendar period is selected.
	for _, q := range []string{"?period=fortnight", "?period=", ""} {
		rec = getReportsPage(t, mux, q)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", q, rec.Code)
		}
		body = rec.Body.String()
		if !strings.Contains(body, `value="14" selected`) {
			t.Fatalf("%s: expected the 14-day rolling default selected, got: %s", q, body)
		}
		if strings.Contains(body, `value="day" selected`) ||
			strings.Contains(body, `value="week" selected`) ||
			strings.Contains(body, `value="month" selected`) ||
			strings.Contains(body, `value="year" selected`) {
			t.Fatalf("%s: no calendar period may be selected in rolling mode, got: %s", q, body)
		}
	}

	// ?period beats ?days when both are present (the two selects submit
	// independently; a stale days param must not disable the period).
	rec = getReportsPage(t, mux, "?period=month&days=30")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `value="month" selected`) {
		t.Fatalf("expected month selected when both params present, got: %s", rec.Body.String())
	}
}

func TestReportsTab_AcceptsPeriodParam(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReportsPageTestDeps(t)
	for _, name := range []string{"sales-trend", "items", "tax", "payments"} {
		rec := getReportsTab(t, mux, name, "?period=week")
		if rec.Code != http.StatusOK {
			t.Fatalf("tab %s with ?period=week: expected 200, got %d: %s", name, rec.Code, rec.Body.String())
		}
		// And the legacy days-only form still works (period="" fallback).
		rec = getReportsTab(t, mux, name, "?days=14&period=")
		if rec.Code != http.StatusOK {
			t.Fatalf("tab %s with legacy days: expected 200, got %d", name, rec.Code)
		}
	}
}

// The business-day-start setting ("reports.business_day_start", local HH:MM,
// default 00:00) is saved through the same manager settings endpoint as the
// EOD schedule, and read back as a Duration offset from local midnight.
func TestBusinessDayStartSettingRoundTrip(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	registerEODAPI(mux, dp)
	ctx := t.Context()

	post := func(form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/settings/eod", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Unset: defaults to midnight.
	if got := reportBusinessDayStart(ctx, dp); got != 0 {
		t.Fatalf("unset business day start = %s, want 0", got)
	}

	// Valid HH:MM persists and parses to the offset.
	rec := post(url.Values{"business_day_start": {"04:30"}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save 04:30: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if v, _, _ := dp.Settings.Get(ctx, keyBusinessDayStart); v != "04:30" {
		t.Fatalf("stored setting = %q, want 04:30", v)
	}
	if got, want := reportBusinessDayStart(ctx, dp), 4*time.Hour+30*time.Minute; got != want {
		t.Fatalf("parsed business day start = %s, want %s", got, want)
	}

	// Invalid values are rejected with 400 and leave the setting untouched.
	for _, bad := range []string{"25:00", "4:30", "04:60", "garbage"} {
		rec = post(url.Values{"business_day_start": {bad}})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("save %q: expected 400, got %d", bad, rec.Code)
		}
	}
	if v, _, _ := dp.Settings.Get(ctx, keyBusinessDayStart); v != "04:30" {
		t.Fatalf("invalid save must not clobber the setting, got %q", v)
	}

	// Empty clears back to the default.
	rec = post(url.Values{"business_day_start": {""}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("clear: expected 204, got %d", rec.Code)
	}
	if got := reportBusinessDayStart(ctx, dp); got != 0 {
		t.Fatalf("cleared business day start = %s, want 0", got)
	}

	// An invalid stored value (e.g. hand-edited DB) falls back to 0 rather
	// than erroring.
	if err := dp.Settings.Set(ctx, keyBusinessDayStart, "not-a-time"); err != nil {
		t.Fatal(err)
	}
	if got := reportBusinessDayStart(ctx, dp); got != 0 {
		t.Fatalf("invalid stored value = %s, want fallback 0", got)
	}
}

// The EOD tab partial renders the business-day-start editor with the saved
// value (manager-gated like the rest of the tab).
func TestReportsTabEOD_ShowsBusinessDayStart(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	if err := dp.Settings.Set(t.Context(), keyBusinessDayStart, "05:00"); err != nil {
		t.Fatal(err)
	}
	rec := getReportsTab(t, mux, "eod", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="business_day_start"`) || !strings.Contains(body, `value="05:00"`) {
		t.Fatalf("expected the business-day-start input with the saved value, got: %s", body)
	}
}
