// Command checkhelpbreadcrumbs is the Go half of guard-help-breadcrumbs.sh
// (ut-docs#4696). A help topic names a settings section with a breadcrumb
// ("Settings → Display"). The translated topic must call that section what
// the till's own translated UI calls it, otherwise the reader looks for a
// label that is not on screen. guard-help-drift only compares structure, so
// it cannot see this.
//
// For each locale under web/help (except en and img) the guard:
//
//  1. finds every `Settings → <Section>` breadcrumb in the English topic,
//     per top-level numbered list item;
//  2. maps the English section name to its web/locales/en.json key (a
//     `settings.*.title` / `.label` key whose value is short);
//  3. requires the same-ordinal numbered item of the translated topic to
//     contain that key's value in the locale's JSON.
//
// A topic whose English and translated numbered-item counts differ is
// skipped (guard-help-drift reports structure). Quoted action/button
// labels are out of scope.
//
// Files are read from disk under -root, not from the embedded FS, so the
// sandboxed self-test can plant files. Known mismatches are recorded in
// scripts/ci/i18n-baseline/help-breadcrumb-baseline.txt; an entry that no
// longer matches a current mismatch is stale and fails (shrink-only).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	baselineRel = "scripts/ci/i18n-baseline/help-breadcrumb-baseline.txt"
	maxLabelLen = 40
	crumb       = "Settings → "
)

var numberedRe = regexp.MustCompile(`^\d+\. `)

// item is one top-level numbered list item.
type item struct {
	line int // 1-based line number in the file of the item's first line
	text string
}

// parseItems returns the top-level numbered items of a topic, skipping the
// leading front matter block.
func parseItems(src string) []item {
	lines := strings.Split(src, "\n")
	start := 0
	if len(lines) > 0 && strings.TrimRight(lines[0], "\r") == "---" {
		for i := 1; i < len(lines); i++ {
			if strings.TrimRight(lines[i], "\r") == "---" {
				start = i + 1
				break
			}
		}
	}
	var items []item
	cur := -1
	for i := start; i < len(lines); i++ {
		l := strings.TrimRight(lines[i], "\r")
		switch {
		case numberedRe.MatchString(l):
			items = append(items, item{line: i + 1, text: l})
			cur = len(items) - 1
		case cur >= 0 && (l == "" || l[0] == ' ' || l[0] == '\t'):
			items[cur].text += "\n" + l
		default:
			cur = -1
		}
	}
	return items
}

// labelMap maps an English label value to its key, from the flat en.json.
func labelMap(en map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range en {
		if !strings.HasPrefix(k, "settings.") || !(strings.HasSuffix(k, ".title") || strings.HasSuffix(k, ".label")) {
			continue
		}
		if v == "" || utf8.RuneCountInString(v) > maxLabelLen {
			continue
		}
		if old, ok := out[v]; ok {
			dk, do := strings.Count(k, "."), strings.Count(old, ".")
			if dk > do || (dk == do && k > old) {
				continue
			}
		}
		out[v] = k
	}
	return out
}

// breadcrumb is one English `Settings → X` naming a mapped label.
type breadcrumb struct {
	label string // English value
	key   string
}

// findBreadcrumbs scans an English item for `Settings → ` occurrences and
// returns the longest word-bounded label following each.
func findBreadcrumbs(text string, labels map[string]string) []breadcrumb {
	var out []breadcrumb
	rest := text
	for {
		i := strings.Index(rest, crumb)
		if i < 0 {
			return out
		}
		rest = rest[i+len(crumb):]
		after := strings.TrimPrefix(rest, "**")
		best := ""
		for v := range labels {
			if len(v) <= len(best) || !strings.HasPrefix(after, v) {
				continue
			}
			r, _ := utf8.DecodeRuneInString(after[len(v):])
			if after[len(v):] != "" && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
				continue
			}
			best = v
		}
		if best != "" {
			out = append(out, breadcrumb{label: best, key: labels[best]})
		}
	}
}

func readFlatJSON(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return m, nil
}

// localeJSON locates a locale's strings: web/locales, else a language-pack
// checkout (same convention as scripts/ci/audit-locale-render.sh).
func localeJSON(root, loc string) (string, bool) {
	cands := []string{
		filepath.Join(root, "web", "locales", loc+".json"),
	}
	if d := os.Getenv("UT_" + strings.ToUpper(loc) + "_PACK_DIR"); d != "" {
		cands = append(cands, filepath.Join(d, "locales", loc+".json"))
	}
	cands = append(cands, filepath.Join(root, "..", "ut-plugin-language-"+loc, "locales", loc+".json"))
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, true
		}
	}
	return "", false
}

// failure is one mismatch.
type failure struct {
	path     string // repo-relative, slash form
	line     int
	loc      string
	topic    string // file name, e.g. display.md
	n        int
	key      string
	english  string
	expected string
}

func (f failure) id() string { return fmt.Sprintf("%s/%s:%d:%s", f.loc, f.topic, f.n, f.key) }

func (f failure) String() string {
	return fmt.Sprintf("%s:%d: item %d names \"Settings → %s\" (%s); expected \"%s\" in the %s item",
		f.path, f.line, f.n, f.english, f.key, f.expected, f.loc)
}

// checkTopic compares one English/translated topic pair. aligned is false
// when the numbered-item counts differ: items can't be paired by ordinal,
// so the pair is not checked (guard-help-drift owns that structure).
func checkTopic(loc, topic, relPath, enSrc, locSrc string, labels, locStrings, enStrings map[string]string) (fails []failure, aligned bool) {
	enItems, locItems := parseItems(enSrc), parseItems(locSrc)
	if len(enItems) != len(locItems) {
		return nil, false
	}
	var out []failure
	for i, ei := range enItems {
		for _, b := range findBreadcrumbs(ei.text, labels) {
			want, ok := locStrings[b.key]
			if !ok || want == "" {
				want = enStrings[b.key]
			}
			if strings.Contains(locItems[i].text, want) {
				continue
			}
			out = append(out, failure{
				path: relPath, line: locItems[i].line, loc: loc, topic: topic,
				n: i + 1, key: b.key, english: b.label, expected: want,
			})
		}
	}
	return out, true
}

// loadBaseline parses the baseline: `<loc>/<topic>.md:<n>:<key> <reason>`.
func loadBaseline(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	entryRe := regexp.MustCompile(`^[a-z][a-z0-9_-]*/[A-Za-z0-9._-]+\.md:\d+:[a-z0-9_.]+$`)
	for i, raw := range strings.Split(string(data), "\n") {
		l := strings.TrimSpace(raw)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		id, reason, _ := strings.Cut(l, " ")
		if !entryRe.MatchString(id) || strings.TrimSpace(reason) == "" {
			return nil, fmt.Errorf("%s:%d: malformed entry %q (want `<loc>/<topic>.md:<item n>:<key> <reason>`)", path, i+1, l)
		}
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("%s:%d: duplicate entry %s", path, i+1, id)
		}
		out[id] = strings.TrimSpace(reason)
	}
	return out, nil
}

func run(root string, stdout, stderr io.Writer) int {
	enStrings, err := readFlatJSON(filepath.Join(root, "web", "locales", "en.json"))
	if err != nil {
		fmt.Fprintf(stderr, "guard-help-breadcrumbs: %v\n", err)
		return 1
	}
	labels := labelMap(enStrings)
	baseline, err := loadBaseline(filepath.Join(root, filepath.FromSlash(baselineRel)))
	if err != nil {
		fmt.Fprintf(stderr, "guard-help-breadcrumbs: %v\n", err)
		return 1
	}

	helpDir := filepath.Join(root, "web", "help")
	dirs, err := os.ReadDir(helpDir)
	if err != nil {
		fmt.Fprintf(stderr, "guard-help-breadcrumbs: %v\n", err)
		return 1
	}
	enTopics, _ := filepath.Glob(filepath.Join(helpDir, "en", "*.md"))
	sort.Strings(enTopics)

	var fails []failure
	used := map[string]bool{}
	checked, unaligned := 0, 0
	// A baseline entry for a locale this run could not load (a language
	// pack not checked out, as in CI) can't be judged stale: it is only
	// checkable where the pack is present (review finding, ut-docs#4696).
	skippedLoc := map[string]bool{}
	for _, d := range dirs {
		loc := d.Name()
		if !d.IsDir() || loc == "en" || loc == "img" {
			continue
		}
		jp, ok := localeJSON(root, loc)
		if !ok {
			fmt.Fprintf(stdout, "notice: guard-help-breadcrumbs: no %s.json (web/locales, UT_%s_PACK_DIR or sibling ut-plugin-language-%s) -- skipping locale %s\n",
				loc, strings.ToUpper(loc), loc, loc)
			skippedLoc[loc] = true
			continue
		}
		locStrings, err := readFlatJSON(jp)
		if err != nil {
			fmt.Fprintf(stderr, "guard-help-breadcrumbs: %v\n", err)
			return 1
		}
		for _, ep := range enTopics {
			topic := filepath.Base(ep)
			lp := filepath.Join(helpDir, loc, topic)
			locSrc, err := os.ReadFile(lp)
			if err != nil {
				continue // missing file: guard-help-topics covers it
			}
			enSrc, err := os.ReadFile(ep)
			if err != nil {
				fmt.Fprintf(stderr, "guard-help-breadcrumbs: %v\n", err)
				return 1
			}
			rel := "web/help/" + loc + "/" + topic
			got, aligned := checkTopic(loc, topic, rel, string(enSrc), string(locSrc), labels, locStrings, enStrings)
			if !aligned {
				unaligned++
				fmt.Fprintf(stdout, "notice: guard-help-breadcrumbs: %s numbered items don't line up with English -- not checked\n", rel)
				continue
			}
			checked++
			fails = append(fails, got...)
		}
	}

	bad := false
	for _, f := range fails {
		if reason, ok := baseline[f.id()]; ok {
			used[f.id()] = true
			fmt.Fprintf(stdout, "known: %s (baseline: %s)\n", f, reason)
			continue
		}
		fmt.Fprintf(stderr, "guard-help-breadcrumbs: %s\n", f)
		bad = true
	}
	ids := make([]string, 0, len(baseline))
	for id := range baseline {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		loc, _, _ := strings.Cut(id, "/")
		if !used[id] && !skippedLoc[loc] {
			fmt.Fprintf(stderr, "guard-help-breadcrumbs: stale baseline entry %s matches no current mismatch -- delete the line from %s\n", id, baselineRel)
			bad = true
		}
	}
	if bad {
		return 1
	}
	fmt.Fprintf(stdout, "✓ help-breadcrumb guard: %d translated topics checked (%d not aligned, skipped); every Settings → breadcrumb uses the locale's own label (or is baselined)\n", checked, unaligned)
	return 0
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	os.Exit(run(*root, os.Stdout, os.Stderr))
}
