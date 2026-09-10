package httpx

import (
	"html/template"
	"strings"
	"testing"
)

// ut-docs#2010: {{ dict "k" v ... }} is what lets a page hand per-call
// parameters to the shared list_header / record_dialog partials with a
// template-only change — no Go edit per adopting screen. Its two error
// paths are pinned because a template that mis-calls it must fail at
// execute time with a readable message, never build a half-filled map.
func TestDictBuildsMap(t *testing.T) {
	got, err := dict("a", 1, "b", "two")
	if err != nil {
		t.Fatalf("dict: %v", err)
	}
	if got["a"] != 1 || got["b"] != "two" || len(got) != 2 {
		t.Fatalf("dict = %#v", got)
	}
}

func TestDictOddArgumentsIsAnError(t *testing.T) {
	if _, err := dict("a", 1, "b"); err == nil {
		t.Fatal("odd-length argument list must be an error")
	}
}

func TestDictNonStringKeyIsAnError(t *testing.T) {
	if _, err := dict(1, "a"); err == nil {
		t.Fatal("non-string key must be an error")
	}
}

// The FuncMap actually exposes it to templates, and a template using it
// reads the values back by field name.
func TestDictIsATemplateFunc(t *testing.T) {
	tpl, err := template.New("t").Funcs(FuncsFor("en")).Parse(`{{ template "p" (dict "id" "x1" "n" 2) }}{{ define "p" }}{{ .id }}:{{ .n }}{{ end }}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var sb strings.Builder
	if err := tpl.Execute(&sb, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if sb.String() != "x1:2" {
		t.Fatalf("got %q, want %q", sb.String(), "x1:2")
	}
}
