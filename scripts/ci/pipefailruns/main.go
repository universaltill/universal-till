// Command pipefailruns is the workflow half of guard-pipefail-grep-q.sh
// (ut-docs#2983). It finds the GitHub Actions `run:` steps that execute
// under `pipefail` and writes each body to its own file, so the guard can
// scan them with the same rule it applies to scripts/ci/*.sh.
//
// A step runs under pipefail when its effective shell (step `shell:`, else
// the job's `defaults.run.shell`, else the workflow's) is exactly `bash` —
// Actions runs that as `bash --noprofile --norc -eo pipefail {0}` — or a
// custom shell string naming pipefail, or when the body itself runs
// `set … -o pipefail`. The implicit default shell is `bash -e {0}`, with no
// pipefail, so such a step is skipped unless its body sets it.
//
//	go run ./scripts/ci/pipefailruns <workflows-dir> <out-dir>
//
// prints one line per extracted step: `<n>.sh<TAB><workflow><TAB><line>`,
// where <line> is the workflow line holding the body's first line.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// pipefailSet matches a `set` command that turns pipefail on — `set -o
// pipefail`, `set -euo pipefail`, `set -o errexit -o pipefail`, and one
// after a `;` (`set -e; set -o pipefail`) — but not inside a # comment.
var pipefailSet = regexp.MustCompile(`(?m)(?:^|;)[ \t]*set\b[^\n#]*-[a-zA-Z]*o[ \t]+pipefail\b`)

type runDefaults struct {
	Run struct {
		Shell string `yaml:"shell"`
	} `yaml:"run"`
}

type step struct {
	Shell string    `yaml:"shell"`
	Run   yaml.Node `yaml:"run"`
}

type job struct {
	Defaults runDefaults `yaml:"defaults"`
	Steps    []step      `yaml:"steps"`
}

type workflow struct {
	Defaults runDefaults `yaml:"defaults"`
	Jobs     yaml.Node   `yaml:"jobs"` // a node, to keep the file's job order
}

// runStep is one extracted `run:` body; Line is the workflow line that
// holds the body's first line.
type runStep struct {
	File string
	Line int
	Body string
}

func underPipefail(shell, body string) bool {
	shell = strings.TrimSpace(shell)
	return shell == "bash" || strings.Contains(shell, "pipefail") || pipefailSet.MatchString(body)
}

func extract(file string, data []byte) ([]runStep, error) {
	var wf workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if wf.Jobs.Kind != yaml.MappingNode {
		return nil, nil
	}
	var out []runStep
	for i := 1; i < len(wf.Jobs.Content); i += 2 {
		var j job
		if err := wf.Jobs.Content[i].Decode(&j); err != nil {
			return nil, fmt.Errorf("%s: job %s: %w", file, wf.Jobs.Content[i-1].Value, err)
		}
		for _, s := range j.Steps {
			if s.Run.Kind != yaml.ScalarNode {
				continue
			}
			shell := s.Shell
			if shell == "" {
				shell = j.Defaults.Run.Shell
			}
			if shell == "" {
				shell = wf.Defaults.Run.Shell
			}
			if !underPipefail(shell, s.Run.Value) {
				continue
			}
			// Exact for literal `|` bodies. yaml.v3 folds `>`, multi-line
			// plain and quoted scalars, so a hit there is reported at the
			// body's first line.
			line := s.Run.Line // plain or quoted: the body starts on the key's line
			if s.Run.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
				line++ // block scalar: the body starts below the `|`/`>` indicator
			}
			out = append(out, runStep{File: file, Line: line, Body: s.Run.Value})
		}
	}
	return out, nil
}

func run(wfDir, outDir string, index io.Writer) error {
	var files []string
	for _, pat := range []string{"*.yml", "*.yaml"} {
		m, err := filepath.Glob(filepath.Join(wfDir, pat))
		if err != nil {
			return err
		}
		files = append(files, m...)
	}
	sort.Strings(files)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	n := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		steps, err := extract(f, data)
		if err != nil {
			return err
		}
		for _, s := range steps {
			n++
			name := fmt.Sprintf("%d.sh", n)
			body := s.Body
			if !strings.HasSuffix(body, "\n") {
				body += "\n"
			}
			if err := os.WriteFile(filepath.Join(outDir, name), []byte(body), 0o644); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(index, "%s\t%s\t%d\n", name, s.File, s.Line); err != nil {
				return err
			}
		}
	}
	return nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: pipefailruns <workflows-dir> <out-dir>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "pipefailruns:", err)
		os.Exit(1)
	}
}
