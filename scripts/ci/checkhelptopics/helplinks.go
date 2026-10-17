package main

// helpLink typo guard (ut-docs#381, follow-up to ut-docs#326).
//
// {{ helpLink "topic-id" }} (internal/httpx.helpLinkHTML) deliberately
// degrades an unknown id to the plain /help index link instead of failing, so
// a bad id can never take the till down mid-render. The flip side is that a
// typo'd id ("backupz") ships silently: the "?" link just points at the wrong
// place. Only settings.html's calls were pinned, by a Go test. This scan
// closes that for every template under web/ui.
//
// It checks LITERAL ids only — {{ helpLink "id" }} / {{ helpLink `id` }}.
// A computed argument ({{ helpLink . }}, confirm_prompt.html's HelpTopic) has
// no id to check at build time and is skipped; the Go callers that fill such
// a field are covered by their own tests. A call spelled with the id piped in
// ({{ "id" | helpLink }}) is likewise not matched — no template uses that
// form, and it would take a new scan to support it.
//
// Only template ACTIONS are scanned, so prose and comments that merely
// mention the word (an HTML comment, {{/* helpLink "x" */}}) never trip it.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type helpLinkRef struct {
	File string // path as walked from the scan root
	Line int    // 1-based line of the id literal
	ID   string
}

var (
	// A whole template action, across lines: {{ ... }} / {{- ... -}}.
	tmplActionRE = regexp.MustCompile(`(?s)\{\{-?(.*?)-?\}\}`)
	// helpLink with a double-quoted or raw-string literal first argument.
	// The \s+ after the name keeps helpLinkNot out; the (?:^|[^.\w]) prefix
	// keeps .helpLink / $.helpLink / xhelpLink (a field or longer name) out.
	helpLinkLiteralRE = regexp.MustCompile("(?:^|[^.\\w])helpLink\\s+(?:\"([^\"\\\\]*)\"|`([^`]*)`)")
)

// helpLinkRefs returns every literal helpLink id under root's *.html files,
// ordered by file then position.
func helpLinkRefs(root string) ([]helpLinkRef, error) {
	var refs []helpLinkRef
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		for _, loc := range tmplActionRE.FindAllStringSubmatchIndex(src, -1) {
			body := src[loc[2]:loc[3]]
			if strings.HasPrefix(strings.TrimSpace(body), "/*") {
				continue // {{/* comment */}}
			}
			for _, m := range helpLinkLiteralRE.FindAllStringSubmatchIndex(body, -1) {
				id, start := "", 0
				if m[2] >= 0 {
					id, start = body[m[2]:m[3]], m[2]
				} else {
					id, start = body[m[4]:m[5]], m[4]
				}
				line := 1 + strings.Count(src[:loc[2]+start], "\n")
				refs = append(refs, helpLinkRef{File: path, Line: line, ID: id})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].File != refs[j].File {
			return refs[i].File < refs[j].File
		}
		return refs[i].Line < refs[j].Line
	})
	return refs, nil
}

// unknownHelpLinks returns one failure line per ref whose id is not in
// known, naming file:line, the bad id and, for a near miss, the topic it
// probably meant.
func unknownHelpLinks(refs []helpLinkRef, known []string) []string {
	set := make(map[string]bool, len(known))
	for _, k := range known {
		set[k] = true
	}
	var out []string
	for _, r := range refs {
		if set[r.ID] {
			continue
		}
		msg := fmt.Sprintf("%s:%d: helpLink %q matches no manual topic", filepath.ToSlash(r.File), r.Line, r.ID)
		if near := nearestTopic(r.ID, known); near != "" {
			msg += fmt.Sprintf(` (did you mean %q?)`, near)
		}
		out = append(out, msg)
	}
	return out
}

// nearestTopic returns the known id within edit distance 2 of id (and at most
// a third of its length), or "" when nothing is close enough to suggest.
func nearestTopic(id string, known []string) string {
	best, bestD := "", 3
	for _, k := range known {
		if d := editDistance(id, k); d < bestD && d*3 <= len(id) {
			best, bestD = k, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
