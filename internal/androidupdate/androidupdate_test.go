package androidupdate

import (
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

func reset(t *testing.T) {
	t.Helper()
	logging.ResetRecent()
	ReportAccepted()
	t.Cleanup(func() { logging.ResetRecent(); ReportAccepted() })
}

func open() []logging.Problem {
	var out []logging.Problem
	for _, p := range logging.OpenProblems(time.Now(), 0) {
		if p.Key == ProblemKeyRefused {
			out = append(out, p)
		}
	}
	return out
}

func TestReportRefused_RaisesKeyedWarn(t *testing.T) {
	reset(t)
	ReportRefused("signer_not_pinned")
	got := open()
	if len(got) != 1 {
		t.Fatalf("open problems = %d, want 1", len(got))
	}
	if got[0].Level != "WARN" {
		t.Fatalf("level = %q, want WARN", got[0].Level)
	}
	if !strings.Contains(got[0].Msg, "signer_not_pinned") {
		t.Fatalf("msg %q does not name the class", got[0].Msg)
	}
	if regexp.MustCompile(`[0-9a-f]{64}`).MatchString(got[0].Msg) {
		t.Fatalf("msg %q carries a digest", got[0].Msg)
	}
}

func TestReportRefused_RepeatSameClassLogsInfo(t *testing.T) {
	reset(t)
	ReportRefused("signer_not_pinned")
	ReportRefused("signer_not_pinned")
	if n := len(open()); n != 1 {
		t.Fatalf("open problems = %d, want 1", n)
	}
	warns := 0
	for _, p := range logging.Recent() {
		if p.Key == ProblemKeyRefused {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("keyed ring entries = %d, want 1", warns)
	}
}

func TestReportRefused_NewClassReplacesOpenOne(t *testing.T) {
	reset(t)
	ReportRefused("not_newer")
	ReportRefused("signer_not_pinned")
	got := open()
	if len(got) != 1 || !strings.Contains(got[0].Msg, "signer_not_pinned") {
		t.Fatalf("open = %+v, want one signer_not_pinned entry", got)
	}
}

func TestReportRefused_UnknownClassNotEchoed(t *testing.T) {
	for _, in := range []string{strings.Repeat("a", 300), "x\nINJECT", "<script>", ""} {
		reset(t)
		ReportRefused(in)
		got := open()
		if len(got) != 1 {
			t.Fatalf("input %q: open = %d, want 1", in, len(got))
		}
		if !strings.Contains(got[0].Msg, "(check_error)") {
			t.Fatalf("input %q: msg %q lacks (check_error)", in, got[0].Msg)
		}
		if in != "" && strings.Contains(got[0].Msg, in) {
			t.Fatalf("input %q echoed in %q", in, got[0].Msg)
		}
		if strings.Contains(got[0].Msg, "INJECT") {
			t.Fatalf("input leaked into %q", got[0].Msg)
		}
	}
}

func TestReportAccepted_ResolvesIt(t *testing.T) {
	reset(t)
	ReportRefused("signer_not_pinned")
	ReportAccepted()
	if n := len(open()); n != 0 {
		t.Fatalf("open after accept = %d, want 0", n)
	}
	found := false
	for _, p := range logging.Recent() {
		if p.Key == ProblemKeyRefused {
			found = true
		}
	}
	if !found {
		t.Fatal("resolved entry should stay in Recent()")
	}
}

// Pins the cross-language set: UpdateLogic.kt's RefuseClass wire values.
func TestClassesAreTheKotlinSet(t *testing.T) {
	want := []string{"check_error", "multiple_signers", "not_newer", "signer_not_pinned", "signer_unreadable", "unreadable_apk", "wrong_package"}
	var got []string
	for k := range classes {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("classes = %v, want %v", got, want)
	}
}
