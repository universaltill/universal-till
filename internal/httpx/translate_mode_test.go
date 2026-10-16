package httpx

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#4370 (ADR-0160 §7): in translate mode the T helper tags each
// rendered string with its key; outside it the output is byte-identical.

func TestMarkKey_RoundTripsAndStaysInvisible(t *testing.T) {
	got := MarkKey("cart.total", "Total")
	if !strings.HasPrefix(got, "Total") {
		t.Fatalf("value must come first, got %q", got)
	}
	if got == "Total" {
		t.Fatal("no marker added")
	}
	keys, plain := unmarkKeys(got)
	if plain != "Total" || len(keys) != 1 || keys[0] != "cart.total" {
		t.Fatalf("unmarkKeys = %q %q", keys, plain)
	}
	// Every marker rune is a default-ignorable Unicode tag character, so a
	// browser paints nothing for it.
	for _, r := range strings.TrimPrefix(got, "Total") {
		if r < 0xE0000 || r > 0xE007F {
			t.Fatalf("marker rune %U is not a tag character", r)
		}
	}
}

func TestMarkKey_NonASCIIKeyLeftUnmarked(t *testing.T) {
	if got := MarkKey("plugin.näme", "x"); got != "x" {
		t.Fatalf("non-ASCII key must not be marked, got %q", got)
	}
}

func renderWithMode(t *testing.T, on bool, tpl string) string {
	t.Helper()
	restore := SnapshotStateForTests()
	t.Cleanup(restore)
	InitTranslateMode(func(*http.Request) bool { return on })
	r := httptest.NewRequest(http.MethodGet, "/sell", nil)
	funcs := withHelpHref(FuncsFor("en"), r)
	tm := template.Must(template.New("x").Funcs(funcs).Parse(tpl))
	var buf bytes.Buffer
	if err := tm.Execute(&buf, nil); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestTranslateMode_OffLeavesTUntouched(t *testing.T) {
	out := renderWithMode(t, false, `<p title="{{ T "common.close" }}">{{ T "common.close" }}</p>{{ if translatemode }}ON{{ end }}`)
	if strings.ContainsRune(out, keyMarkStart) || strings.Contains(out, "ON") {
		t.Fatalf("translate mode off must not mark: %q", out)
	}
}

func TestTranslateMode_OnMarksTextAttributesAndScripts(t *testing.T) {
	out := renderWithMode(t, true, `<p title="{{ T "common.close" }}">{{ T "common.close" }}</p><script>var T={a:"{{ T "common.close" }}"}</script>{{ if translatemode }}ON{{ end }}`)
	if !strings.Contains(out, "ON") {
		t.Fatalf("translatemode func should be true: %q", out)
	}
	// text, attribute and JS-string contexts all carry the key; html/template
	// may escape the marker inside the script, so count the decoded form.
	if n := strings.Count(out, string(keyMarkStart)) + strings.Count(out, `\U000e0001`) + strings.Count(out, `󠀁`); n < 2 {
		t.Fatalf("expected the marker in the text and the attribute at least, got %d in %q", n, out)
	}
}

func TestTranslateMode_NoCheckerMeansOff(t *testing.T) {
	restore := SnapshotStateForTests()
	t.Cleanup(restore)
	InitTranslateMode(nil)
	if TranslateModeOn(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("no checker installed must read as off")
	}
	if TranslateModeOn(nil) {
		t.Fatal("nil request must read as off")
	}
}

func TestTranslateMode_ChangesShellSignature(t *testing.T) {
	off := renderWithMode(t, false, `{{ shellsig "default" }}`)
	on := renderWithMode(t, true, `{{ shellsig "default" }}`)
	if off == on {
		t.Fatal("a translate-mode page must not be boost-swapped into a normal shell (or back)")
	}
}

// unmarkKeys strips every marker from s and returns the keys found, in
// order, plus the plain text. The Go twin of translate-mode.js's decoder,
// used by these tests to read a marker back.
func unmarkKeys(s string) ([]string, string) {
	if !strings.ContainsRune(s, keyMarkStart) {
		return nil, s
	}
	var keys []string
	var plain, key strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == keyMarkStart:
			in = true
			key.Reset()
		case r == keyMarkEnd && in:
			in = false
			keys = append(keys, key.String())
		case in && r > tagBase+0x1F && r < tagBase+0x7F:
			key.WriteByte(byte(r - tagBase))
		case in:
			// A stray rune inside a marker: drop the half-read marker.
			in = false
			plain.WriteRune(r)
		default:
			plain.WriteRune(r)
		}
	}
	return keys, plain.String()
}
