package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/clock"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/problemclass"
	"github.com/universaltill/universal-till/internal/settings"
)

func initProblemsI18n(t *testing.T) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
}

func TestBuildProblemsView_OrderAndCollapse(t *testing.T) {
	initProblemsI18n(t)
	now := time.Now().UTC()
	// newest first, as logging.OpenProblems returns them.
	ps := []logging.Problem{
		{At: now, Level: "WARN", Msg: "something odd"},
		{At: now.Add(-1 * time.Minute), Level: "ERROR", Msg: "[Backup] failed"},
		{At: now.Add(-2 * time.Minute), Level: "WARN", Msg: "[Backup] failed"},
		{At: now.Add(-3 * time.Minute), Level: "WARN", Msg: "wasm sync: boom"},
		{At: now.Add(-4 * time.Minute), Level: "WARN", Msg: "[Backup] failed"},
		{At: now.Add(-5 * time.Minute), Level: "WARN", Msg: "cloudsync: x"},
	}
	v := buildProblemsView("en", ps, "startup-line")
	var order []problemclass.Area
	for _, g := range v.Groups {
		order = append(order, g.Area)
	}
	want := []problemclass.Area{problemclass.AreaCloudSync, problemclass.AreaPlugins, problemclass.AreaDatabase, problemclass.AreaOther}
	if len(order) != len(want) {
		t.Fatalf("groups %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("groups %v, want %v", order, want)
		}
	}
	var db problemGroup
	for _, g := range v.Groups {
		if g.Area == problemclass.AreaDatabase {
			db = g
		}
	}
	if len(db.Lines) != 1 || db.Lines[0].Count != 3 || len(db.Lines[0].Raw) != 3 {
		t.Fatalf("database lines: %+v", db.Lines)
	}
	if !db.Lines[0].Newest.Equal(ps[1].At) || db.Lines[0].Severity != problemclass.SeverityFailed {
		t.Fatalf("collapsed line newest/severity wrong: %+v", db.Lines[0])
	}
	if !strings.Contains(db.Lines[0].CountLabel, "3") {
		t.Fatalf("count label %q", db.Lines[0].CountLabel)
	}
}

func TestBuildProblemsView_NewestFirstInGroup(t *testing.T) {
	initProblemsI18n(t)
	now := time.Now().UTC()
	ps := []logging.Problem{
		{At: now, Level: "WARN", Msg: "[Backup] b"},
		{At: now.Add(-time.Minute), Level: "WARN", Msg: "[Housekeeping] a"},
	}
	v := buildProblemsView("en", ps, "")
	ls := v.Groups[0].Lines
	if len(ls) != 2 || !strings.Contains(ls[0].Raw[0], "[Backup] b") {
		t.Fatalf("lines not newest first: %+v", ls)
	}
}

func TestBuildProblemsView_WorstAndOK(t *testing.T) {
	initProblemsI18n(t)
	now := time.Now().UTC()
	v := buildProblemsView("en", nil, "s")
	if len(v.Groups) != 0 || v.Worst != problemclass.SeverityOK || v.WorstKey != "backoffice.problems.severity.ok" {
		t.Fatalf("empty: %+v", v)
	}
	v = buildProblemsView("en", []logging.Problem{{At: now, Level: "WARN", Msg: "x"}}, "s")
	if v.Worst != problemclass.SeverityAttention {
		t.Fatalf("warn worst %v", v.Worst)
	}
	v = buildProblemsView("en", []logging.Problem{{At: now, Level: "WARN", Msg: "x"}, {At: now, Level: "ERROR", Msg: "y"}}, "s")
	if v.Worst != problemclass.SeverityFailed || v.WorstKey != "backoffice.problems.severity.failed" {
		t.Fatalf("error worst %v", v.Worst)
	}
}

func TestBuildProblemsView_SupportText(t *testing.T) {
	initProblemsI18n(t)
	at := time.Now().UTC().Truncate(time.Second)
	long := "cloudsync: " + strings.Repeat("abcdefghij", 60)
	v := buildProblemsView("en", []logging.Problem{{At: at, Level: "ERROR", Msg: long}}, "UT 1.2.3 startup-line")
	for _, want := range []string{
		"UT 1.2.3 startup-line",
		"Raw log lines",
		"problem report",
		"The till could not sync with the cloud",
		"Not working",
		at.Format(time.RFC3339) + " ERROR " + long,
	} {
		if !strings.Contains(v.SupportText, want) {
			t.Errorf("support text missing %q:\n%s", want, v.SupportText)
		}
	}
	if strings.Contains(v.SupportText, "…") {
		t.Error("support text must not truncate")
	}
	if strings.Contains(v.SupportText, "backoffice.problems") {
		t.Error("support text leaks a raw key")
	}
	empty := buildProblemsView("en", nil, "s")
	if !strings.Contains(empty.SupportText, "No open problems.") || !strings.Contains(empty.SupportText, "Raw log lines") {
		t.Errorf("empty support text:\n%s", empty.SupportText)
	}
}

func TestBackofficePage_RendersReadableProblems(t *testing.T) {
	initProblemsI18n(t)
	t.Setenv("UT_AUTH", "off")
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state, Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerBackofficePage(mux, dp)
	get := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/backoffice", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		return rec.Body.String()
	}

	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	if body := get(); !strings.Contains(body, "Everything OK") || !strings.Contains(body, "problems-copy") {
		t.Fatalf("empty state wrong:\n%s", body)
	}

	l := logging.L()
	l.Warnf("cloudsync: push: cloud answered 403 not_main_till: nope")
	l.Errorf("wasm sync: module closed with exit_code(2)")
	l.WarnProblemf("test.gone", "[Backup] resolved one")
	logging.ResolveProblems("test.gone")
	for i := 0; i < 7; i++ { // more than the old 5-line cap
		l.Warnf("odd line %d", i)
	}
	body := get()
	for _, want := range []string{
		`data-testid="problem-group-cloud_sync"`,
		`data-testid="problem-group-plugins"`,
		"Needs attention", "Not working",
		"open Tills in my. and choose Replace this till",
		"backoffice-support-text",
		"odd line 0", "odd line 6",
		"cloud answered 403 not_main_till", // raw line inside details
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	// The support text repeats every reason, next step and raw line, so
	// check the visible list on its own (tester, ut-docs#6978): the next
	// step is rendered on the row and the raw line sits inside the row's
	// technical-details <pre>, not only in the copy block.
	list := body
	if i := strings.Index(list, `data-testid="problem-group-`); i >= 0 {
		list = list[i:]
	}
	if i := strings.Index(list, `id="backoffice-support-text"`); i >= 0 {
		list = list[:i]
	}
	if !strings.Contains(list, "What to do: If this till should be the main till, open Tills in my. and choose Replace this till. Otherwise there is nothing to do.") {
		t.Error("row must show the next step after its label")
	}
	if !regexp.MustCompile(`(?s)<details>\s*<summary>Show technical details</summary>\s*<pre class="backoffice-problem-raw"[^>]*>[^<]*cloud answered 403 not_main_till`).MatchString(list) {
		t.Error("raw line must render inside the row's technical details")
	}
	if strings.Contains(body, "resolved one") {
		t.Error("resolved problem must not render")
	}
}

func TestPageProblems_AgeCutHitsOnlyUnkeyed(t *testing.T) {
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	l := logging.L()
	l.Warnf("old unkeyed line")
	l.WarnProblemf("test.keyed", "keyed line")
	if got := pageProblems(clock.Now().UTC()); len(got) != 2 {
		t.Fatalf("fresh: %+v", got)
	}
	got := pageProblems(clock.Now().UTC().Add(problemReportMaxAge + time.Hour))
	if len(got) != 1 || got[0].Msg != "keyed line" {
		t.Fatalf("aged: want only the keyed line, got %+v", got)
	}
}

func TestBuildProblemsView_UnclassifiedShowsSnippet(t *testing.T) {
	initProblemsI18n(t)
	long := "weird thing " + strings.Repeat("x", 300)
	v := buildProblemsView("en", []logging.Problem{{At: time.Now(), Level: "WARN", Msg: long}, {At: time.Now(), Level: "WARN", Msg: "[Backup] y"}}, "")
	for _, g := range v.Groups {
		for _, l := range g.Lines {
			if g.Area == problemclass.AreaOther && (l.Snippet == "" || len(l.Snippet) >= len(long)) {
				t.Errorf("other row snippet %q", l.Snippet)
			}
			if g.Area != problemclass.AreaOther && l.Snippet != "" {
				t.Errorf("classified row has snippet %q", l.Snippet)
			}
		}
	}
}

func TestProblemWhen_LocalizedDigits(t *testing.T) {
	initProblemsI18n(t)
	now := time.Date(2030, 3, 5, 12, 0, 0, 0, time.Local) // fixed-date:ok pure formatting, no clock check
	got := problemWhen(now.Add(-time.Hour), now, "fa")
	if strings.ContainsAny(got, "0123456789") {
		t.Errorf("fa time %q still has Latin digits", got)
	}
}

// The startup line (data_dir, log path) reaches the copy text only for a
// settings-permitted user and never on the public demo (review L3,
// ut-docs#6978).
func TestBackofficePage_StartupLineOnlyOutsideDemo(t *testing.T) {
	initProblemsI18n(t)
	t.Setenv("UT_AUTH", "off")
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)
	for _, demo := range []bool{false, true} {
		cfg := &config.Config{Theme: "default", Demo: demo}
		state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
		dp := &common.Deps{Cfg: cfg, Db: db, State: state, Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
		mux := http.NewServeMux()
		registerBackofficePage(mux, dp)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/backoffice", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("demo=%v: status %d", demo, rec.Code)
		}
		has := strings.Contains(rec.Body.String(), "data_dir=")
		if has == demo {
			t.Errorf("demo=%v: startup line present=%v, want %v", demo, has, !demo)
		}
	}
}
