package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/manual"
	uiassets "github.com/universaltill/universal-till/web"
)

func writeTemplate(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Only real {{ helpLink "id" }} actions count: a literal id is collected with
// its file:line, a computed argument (helpLink .) is skipped by design, and
// prose or template comments that merely mention helpLink must not trip it.
func TestHelpLinkRefsFindsLiteralCallsOnly(t *testing.T) {
	root := t.TempDir()
	writeTemplate(t, root, "pages/a.html", `<h1>{{ T "a.title" }} {{ helpLink "backups" }}</h1>
<p>second line</p>
<h2>{{- helpLink "sell" -}}</h2>
<h3>{{ helpLink `+"`raw-id`"+` }}</h3>
`)
	writeTemplate(t, root, "partials/b.html", `<!-- the helpLink beside the prompt is for sighted users -->
{{/* helpLink "in-a-comment" */}}
<h3>{{ with .HelpTopic }} {{ helpLink . }}{{ end }}</h3>
<p>{{ helpLinkNot "also-not" }} {{ .helpLink }} {{ .helpLink "field-call" }} {{ $.helpLink "root-field-call" }}</p>
`)
	writeTemplate(t, root, "pages/notes.txt", `{{ helpLink "ignored-not-html" }}`)

	got, err := helpLinkRefs(root)
	if err != nil {
		t.Fatalf("helpLinkRefs: %v", err)
	}
	var gotIDs []string
	for _, r := range got {
		rel, err := filepath.Rel(root, r.File)
		if err != nil {
			t.Fatal(err)
		}
		gotIDs = append(gotIDs, filepath.ToSlash(rel)+":"+strconv.Itoa(r.Line)+":"+r.ID)
	}
	want := []string{
		"pages/a.html:1:backups",
		"pages/a.html:3:sell",
		"pages/a.html:4:raw-id",
	}
	if !slices.Equal(gotIDs, want) {
		t.Errorf("helpLinkRefs = %v, want %v", gotIDs, want)
	}
}

// A typo'd id fails with the file, the line, the bad id and — when one topic
// is a near miss — the topic it probably meant.
func TestUnknownHelpLinksNamesFileLineAndSuggestion(t *testing.T) {
	known := []string{"backups", "sell", "multitill"}
	refs := []helpLinkRef{
		{File: "web/ui/pages/settings.html", Line: 1037, ID: "backups"},
		{File: "web/ui/pages/registers.html", Line: 8, ID: "multitil"},
		{File: "web/ui/pages/x.html", Line: 2, ID: "zzzzzz"},
	}
	bad := unknownHelpLinks(refs, known)
	if len(bad) != 2 {
		t.Fatalf("want 2 unknown refs, got %d: %v", len(bad), bad)
	}
	if !strings.Contains(bad[0], `web/ui/pages/registers.html:8: helpLink "multitil"`) ||
		!strings.Contains(bad[0], `did you mean "multitill"`) {
		t.Errorf("bad[0] = %q, want file:line, id and a suggestion", bad[0])
	}
	if !strings.Contains(bad[1], `web/ui/pages/x.html:2: helpLink "zzzzzz"`) ||
		strings.Contains(bad[1], "did you mean") {
		t.Errorf("bad[1] = %q, want file:line and id but no suggestion for a far miss", bad[1])
	}
}

// The real templates: every literal helpLink id in web/ui is a real topic of
// the embedded manual. This is the same check the CI guard runs, pinned in
// go test too so a typo fails locally before CI.
func TestEveryRealHelpLinkResolvesToATopic(t *testing.T) {
	lib, err := manual.Load(uiassets.HelpFS, "help")
	if err != nil {
		t.Fatalf("manual.Load: %v", err)
	}
	refs, err := helpLinkRefs(filepath.Join("..", "..", "..", "web", "ui"))
	if err != nil {
		t.Fatalf("helpLinkRefs: %v", err)
	}
	if len(refs) < 30 {
		t.Fatalf("found only %d literal helpLink calls under web/ui; the scanner has stopped matching (there are 40+)", len(refs))
	}
	if bad := unknownHelpLinks(refs, lib.IDs()); len(bad) > 0 {
		t.Errorf("helpLink ids with no manual topic:\n%s", strings.Join(bad, "\n"))
	}
}
