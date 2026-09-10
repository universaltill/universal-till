package httpx

import (
	"html/template"
	"strings"
	"testing"
)

// ut-docs#2010 review (B2): dict fails loudly only on arity / key type; a
// MISSING key renders as "" at HTTP 200. req is the guard each dict-taking
// partial runs on its first line, and it must name the offending key.
func TestReqNamesTheMissingKey(t *testing.T) {
	_, err := req(map[string]any{"id": "x"}, "id", "formID")
	if err == nil {
		t.Fatal("missing key must be an error")
	}
	if !strings.Contains(err.Error(), "formID") {
		t.Fatalf("error does not name the missing key: %v", err)
	}
}

func TestReqRejectsEmptyStringAndNil(t *testing.T) {
	if _, err := req(map[string]any{"a": ""}, "a"); err == nil || !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf(`empty string must fail naming "a", got %v`, err)
	}
	if _, err := req(map[string]any{"a": nil}, "a"); err == nil || !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf(`nil value must fail naming "a", got %v`, err)
	}
	if _, err := req(nil, "a"); err == nil {
		t.Fatal("a nil dict must fail on the first required key")
	}
}

func TestReqPassesWhenEveryKeyIsPresent(t *testing.T) {
	out, err := req(map[string]any{"a": "x", "n": 0, "b": false}, "a", "n", "b")
	if err != nil {
		t.Fatalf("all present: %v", err)
	}
	if out != "" {
		t.Fatalf("req must print nothing when used bare, got %q", out)
	}
}

// The FuncMap exposes it, and the documented first-line form
// {{ $_ := req . "k" }} fails a template execution naming the key.
func TestReqIsATemplateFuncThatFailsExecution(t *testing.T) {
	tpl, err := template.New("t").Funcs(FuncsFor("en")).Parse(
		`{{ define "p" }}{{ $_ := req . "id" "formID" }}[{{ .id }}]{{ end }}{{ template "p" . }}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var sb strings.Builder
	if err := tpl.Execute(&sb, map[string]any{"id": "x", "formID": "f"}); err != nil || sb.String() != "[x]" {
		t.Fatalf("complete dict: out=%q err=%v", sb.String(), err)
	}
	sb.Reset()
	err = tpl.Execute(&sb, map[string]any{"id": "x"})
	if err == nil || !strings.Contains(err.Error(), "formID") {
		t.Fatalf("missing formID must fail execution naming it, got out=%q err=%v", sb.String(), err)
	}
}
