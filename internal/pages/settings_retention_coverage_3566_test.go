package pages

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#3566. The Report retention card's "Records held: <earliest> –
// <latest>" line printed two raw RFC3339 stamps straight into RTL text. With
// no bidi isolation a fa/ar till showed them run together and in the wrong
// visual order. Each value must sit in its own LTR-isolated <bdi>, and be
// rendered with the locale's date/time convention (the `datetime` helper)
// rather than as a raw RFC3339 string.

var coverageLineRE = regexp.MustCompile(`(?s)` + `<bdi dir="ltr" data-testid="retention-coverage-earliest">([^<]*)</bdi>` +
	`\s*–\s*` + `<bdi dir="ltr" data-testid="retention-coverage-latest">([^<]*)</bdi>`)

func seedCoverage(t *testing.T, repo *data.POSRepo, earliest, latest time.Time) {
	t.Helper()
	ctx := context.Background()
	for _, ts := range []time.Time{earliest, latest} {
		period := ts.Format(time.RFC3339)
		if _, err := repo.ArchiveReport(ctx, "eod", period, []byte(`{}`), "", "", ts); err != nil {
			t.Fatalf("archive report %s: %v", period, err)
		}
	}
}

func TestSettingsRetentionCoverage_IsolatedAndLocalized_3566(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	earliest := time.Date(2026, 8, 3, 18, 5, 0, 0, time.Local)
	latest := time.Date(2026, 9, 21, 19, 40, 0, 0, time.Local)
	seedCoverage(t, data.NewPOSRepo(d.Db), earliest, latest)

	for _, loc := range []string{"fa", "ar", "en"} {
		t.Run(loc, func(t *testing.T) {
			body := renderSettingsWith(t, mux, &http.Cookie{Name: "ut_lang", Value: httpx.LocaleOverrideValue(loc)})
			m := coverageLineRE.FindStringSubmatch(body)
			if m == nil {
				i := strings.Index(body, "retention-coverage")
				if i < 0 {
					i = strings.Index(body, "–")
				}
				t.Fatalf("coverage line has no LTR-isolated earliest/latest <bdi> pair; near: %.300s", body[max(i-150, 0):])
			}
			wantEarliest := httpx.FormatDateTime(earliest, loc)
			wantLatest := httpx.FormatDateTime(latest, loc)
			if m[1] != wantEarliest || m[2] != wantLatest {
				t.Fatalf("coverage = %q – %q, want %q – %q (earliest first, locale-formatted)", m[1], m[2], wantEarliest, wantLatest)
			}
			if strings.Contains(m[1], "T18:05") || strings.Contains(m[2], "T19:40") {
				t.Fatalf("coverage still shows a raw RFC3339 stamp: %q – %q", m[1], m[2])
			}
		})
	}
}

// A period that isn't a parseable timestamp (a legacy bare-date "eod" row)
// still renders — raw, but isolated — rather than as an empty cell.
func TestSettingsRetentionCoverage_UnparseablePeriodStaysVisible_3566(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(d.Db)
	for _, p := range []string{"2026-08-03", "2026-09-21"} {
		if _, err := repo.ArchiveReport(ctx, "eod", p, []byte(`{}`), "", "", time.Time{}); err != nil {
			t.Fatalf("archive report %s: %v", p, err)
		}
	}
	body := renderSettingsWith(t, mux, &http.Cookie{Name: "ut_lang", Value: httpx.LocaleOverrideValue("fa")})
	m := coverageLineRE.FindStringSubmatch(body)
	if m == nil || m[1] != "2026-08-03" || m[2] != "2026-09-21" {
		t.Fatalf("bare-date coverage not rendered isolated and verbatim: %v", m)
	}
}
