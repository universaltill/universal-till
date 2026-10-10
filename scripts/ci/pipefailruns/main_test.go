package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractPicksStepsRunningUnderPipefail(t *testing.T) {
	wf := `name: x
on: push
jobs:
  plain:
    runs-on: ubuntu-latest
    steps:
      - run: echo implicit-default-shell
      - name: explicit bash
        shell: bash
        run: |
          echo one
          echo two
      - name: sets pipefail itself
        run: |
          set -euo pipefail
          echo three
      - name: set -o form
        run: |
          set -e -o pipefail
          echo four
      - name: long-option form after a semicolon
        run: |
          set -e; set -o errexit -o pipefail
      - name: commented out
        run: |
          # set -o pipefail
          echo not-pipefail
      - name: pwsh
        shell: pwsh
        run: Write-Output five
      - uses: actions/checkout@v5
  jobdefault:
    runs-on: ubuntu-latest
    defaults:
      run:
        shell: bash
    steps:
      - run: echo six
      - shell: sh
        run: echo seven
      - shell: bash -eo pipefail {0}
        run: echo eight
`
	runs, err := extract("wf.yml", []byte(wf))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range runs {
		got = append(got, strings.TrimSpace(strings.SplitN(r.Body, "\n", 2)[0]))
	}
	want := []string{"echo one", "set -euo pipefail", "set -e -o pipefail", "set -e; set -o errexit -o pipefail", "echo six", "echo eight"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("extracted steps\n got %q\nwant %q", got, want)
	}
}

func TestExtractWorkflowDefaultShell(t *testing.T) {
	wf := `on: push
defaults:
  run:
    shell: bash
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - run: echo inherited
      - shell: sh
        run: echo overridden
`
	runs, err := extract("wf.yml", []byte(wf))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Body != "echo inherited" {
		t.Fatalf("got %+v, want only the step inheriting workflow defaults", runs)
	}
}

func TestExtractLineIsTheBodysFirstLine(t *testing.T) {
	wf := `on: push
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - shell: bash
        run: |
          echo block-line-8
          echo block-line-9
      - shell: bash
        run: echo plain-line-11
`
	runs, err := extract("wf.yml", []byte(wf))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].Line != 8 || runs[1].Line != 11 {
		t.Fatalf("lines = %d, %d; want 8, 11", runs[0].Line, runs[1].Line)
	}
}

func TestExtractRejectsInvalidYAML(t *testing.T) {
	if _, err := extract("bad.yml", []byte("jobs: [unclosed")); err == nil {
		t.Fatal("want an error for invalid YAML, got nil")
	}
}

func TestRunWritesBodiesAndIndex(t *testing.T) {
	wfDir, out := t.TempDir(), t.TempDir()
	wf := "on: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - shell: bash\n        run: echo hi\n"
	if err := os.WriteFile(filepath.Join(wfDir, "a.yml"), []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := run(wfDir, out, &b); err != nil {
		t.Fatal(err)
	}
	want := "1.sh\t" + filepath.Join(wfDir, "a.yml") + "\t7\n"
	if b.String() != want {
		t.Fatalf("index = %q, want %q", b.String(), want)
	}
	body, err := os.ReadFile(filepath.Join(out, "1.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "echo hi\n" {
		t.Fatalf("body = %q", body)
	}
}
