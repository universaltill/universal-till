package bench

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/tools/wasmbench/guests/answer"
)

// want is the answer every bench guest must return for the timed event.
func want(t *testing.T) string {
	t.Helper()
	out, ok := answer.Answer(event)
	if !ok {
		t.Fatal("shared handler does not answer the bench event")
	}
	return string(out)
}

// buildGuest compiles one guest with the plain Go toolchain, exactly as
// build.sh does, so the test exercises the same module shapes the device
// runs measure.
func buildGuest(t *testing.T, pkg string, extra ...string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "guest.wasm")
	args := append([]string{"build"}, extra...)
	args = append(args, "-o", out, "../guests/"+pkg)
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, b)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCommandModuleAnswersEveryEvent(t *testing.T) {
	raw := buildGuest(t, "command")
	for _, engine := range []string{"interpreter", "compiler"} {
		r, err := benchCommand(context.Background(), engine, "go-command", raw, 3)
		if err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		if len(r.Samples) != 3 {
			t.Fatalf("%s: want 3 samples, got %d", engine, len(r.Samples))
		}
		if r.Compile <= 0 || r.Samples[0] <= 0 {
			t.Fatalf("%s: timings not recorded: %+v", engine, r)
		}
		if string(r.Answer) != want(t) {
			t.Fatalf("%s: timed a wrong answer %q", engine, r.Answer)
		}
	}
}

func TestReactorModuleAnswersEveryCall(t *testing.T) {
	raw := buildGuest(t, "reactor", "-buildmode=c-shared")
	for _, engine := range []string{"interpreter", "compiler"} {
		r, err := benchReactor(context.Background(), engine, "go-reactor", raw, 2, 50)
		if err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		if len(r.Instantiate) != 2 || len(r.Calls) != 50 {
			t.Fatalf("%s: want 2 instantiations and 50 calls, got %d/%d", engine, len(r.Instantiate), len(r.Calls))
		}
		if string(r.Answer) != want(t) {
			t.Fatalf("%s: timed a wrong answer %q", engine, r.Answer)
		}
	}
}

// A reactor that answers wrongly must fail the run, not be timed: a
// benchmark of a broken call path measures nothing.
func TestReactorRejectsWrongAnswer(t *testing.T) {
	raw := buildGuest(t, "reactor", "-buildmode=c-shared")
	_, err := benchReactorWith(context.Background(), "interpreter", "go-reactor", raw, 1, 1, []byte(`{"type":"unknown.ask"}`))
	if err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("want a no-answer error for an event the guest does not answer, got %v", err)
	}
}

func TestSummaryPercentiles(t *testing.T) {
	s := summarize([]float64{5, 1, 4, 2, 3})
	if s.N != 5 || s.Min != 1 || s.Median != 3 || s.P95 != 5 || s.Max != 5 {
		t.Fatalf("got %+v", s)
	}
}

// A command guest that writes something other than a JSON answer (an error
// line, say) and still exits 0 must fail the run.
func TestNonJSONStdoutIsNotAnAnswer(t *testing.T) {
	for _, out := range []string{"", "  ", "error: bad event", "[1]", "{broken"} {
		if isAnswer([]byte(out)) {
			t.Errorf("%q accepted as an answer", out)
		}
	}
	if !isAnswer([]byte(" {\"ok\":true}\n")) {
		t.Error("a JSON object was rejected")
	}
}
