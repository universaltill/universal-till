// Command checkhelpdrift is the Go half of guard-help-drift.sh
// (ut-docs#1962). guard-help-topics.sh already guarantees every shipped
// locale has *a* translated file for every topic English has — but it only
// checks the file exists, never that its content still says what English
// says. A translated topic can silently rot: English grows a new bullet or
// a rewritten step, the translations don't, and guard-help-topics.sh stays
// green throughout.
//
// This guard compares each non-English topic against its English original
// on cheap, language-independent STRUCTURE: heading count, numbered-step
// count, indented-bullet count, top-level-bullet count, and the count of
// list items that lead with a bold label (`**Term:** ...`). It cannot prove
// a translation is accurate — nothing automated can — but a topic that grew
// a whole new bullet or lost one always changes at least one of these
// counts, and every real drift instance found in ut-docs#1962 would have
// failed this check the day it was introduced.
//
// Like checkhelptopics, this calls the real internal/manual package to load
// topic content rather than re-parsing front matter itself.
//
// A locale/topic pair whose structure doesn't match English is not
// automatically a failure: scripts/ci/i18n-baseline/help-drift-baseline.json
// records already-known drift (mirrors the ut-plugin-language-*
// i18n-baseline/ convention for already-known untranslated keys), so
// existing gaps can be burned down over time instead of blocking every PR
// from day one. But a baseline entry is a live claim about the *current*
// mismatch, not a permanent exemption: if the real drift no longer matches
// what the baseline recorded — because someone fixed it (so there's no
// mismatch left to allow), because the translation changed into a
// different mismatch, or because ENGLISH ITSELF moved since the entry was
// recorded (a baselined pair is otherwise silently exempt from ever
// noticing English grew further away — independent review finding,
// ut-docs#1962) — the guard fails and says so, so the baseline is never
// quietly wrong.
//
// Run with -update to refresh already-recorded entries after a translation
// fix: it recomputes each existing entry's signature/english fields, drops
// an entry outright once its topic/locale no longer differs from English
// (the normal way a baseline entry disappears once burned down), and drops
// one whose topic/locale no longer exists at all. It deliberately never
// ADDS a new entry for previously-unrecorded drift — that needs a human
// picking the `reason` text, not an automatic tool.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/manual"
	uiassets "github.com/universaltill/universal-till/web"
)

// baselinePath deliberately lives outside web/help/ (independent review
// finding, ut-docs#1962): that whole tree is //go:embed'd into every POS/
// desktop/Android binary (web/embed.go), and this file is CI-only
// bookkeeping nothing at runtime ever reads.
const baselinePath = "scripts/ci/i18n-baseline/help-drift-baseline.json"

// signature is the structural fingerprint of one topic's Markdown body, in
// one locale. Every field is a count, deliberately: content differs by
// language, but "the two versions have the same NUMBER of X" is
// language-independent and cheap.
type signature struct {
	Headings     int `json:"headings"`
	NumberedStep int `json:"numbered_steps"`
	SubBullets   int `json:"sub_bullets"`
	TopBullets   int `json:"top_bullets"`
	BoldLeadins  int `json:"bold_leadins"`
}

func (s signature) String() string {
	return fmt.Sprintf("headings=%d numbered_steps=%d sub_bullets=%d top_bullets=%d bold_leadins=%d",
		s.Headings, s.NumberedStep, s.SubBullets, s.TopBullets, s.BoldLeadins)
}

var (
	headingRe    = regexp.MustCompile(`^#{1,6}\s`)
	numberedRe   = regexp.MustCompile(`^\d+\.\s`)
	subBulletRe  = regexp.MustCompile(`^[ \t]+[-*]\s`)
	topBulletRe  = regexp.MustCompile(`^[-*]\s`)
	boldLeadInRe = regexp.MustCompile(`^[ \t]*[-*]?[ \t]*(?:\d+\.)?[ \t]*\*\*[^*]+\*\*`)
)

// computeSignature counts structural markers in a topic's Markdown body.
// Counts, not text, so it never depends on which language the body is in.
func computeSignature(markdown string) signature {
	var sig signature
	for _, line := range strings.Split(markdown, "\n") {
		switch {
		case headingRe.MatchString(line):
			sig.Headings++
		case numberedRe.MatchString(line):
			sig.NumberedStep++
		case subBulletRe.MatchString(line):
			sig.SubBullets++
		case topBulletRe.MatchString(line):
			sig.TopBullets++
		}
		// Bold-lead-in is independent of the switch above: a numbered step,
		// a top-level bullet and a sub-bullet can all lead with **Term:**.
		if boldLeadInRe.MatchString(line) {
			sig.BoldLeadins++
		}
	}
	return sig
}

// baselineEntry is one recorded, already-known drift instance. It anchors
// BOTH sides of the comparison it's exempting, not just the locale's: a
// prior version of this guard recorded only the locale's signature, which
// meant English growing an entirely new section left a baselined pair
// silently exempt forever — the locale signature never changed, so it kept
// matching the recording (independent review finding, ut-docs#1962). Anchor
// signature is the locale's, at the time this entry was recorded; English
// is what the topic's English original looked like at the same time.
type baselineEntry struct {
	Locale    string    `json:"locale"`
	Topic     string    `json:"topic"`
	Signature signature `json:"signature"`
	English   signature `json:"english"`
	Reason    string    `json:"reason"`
}

type baselineFile struct {
	Entries []baselineEntry `json:"entries"`
}

func loadBaseline(path string) (map[[2]string]baselineEntry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[[2]string]baselineEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var bf baselineFile
	if err := json.Unmarshal(data, &bf); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	byKey := make(map[[2]string]baselineEntry, len(bf.Entries))
	for _, e := range bf.Entries {
		key := [2]string{e.Locale, e.Topic}
		if _, dup := byKey[key]; dup {
			return nil, fmt.Errorf("%s: duplicate entry for locale %q topic %q", path, e.Locale, e.Topic)
		}
		byKey[key] = e
	}
	return byKey, nil
}

// finding is one line this guard wants to say, and whether it's fatal.
// Splitting "what to say" from "print it and exit" is what makes checkAll
// testable without shelling out to the built binary.
type finding struct {
	msg  string
	fail bool
}

// checkAll compares every translated topic against its English original and
// reports one finding per topic/locale pair worth mentioning (a drift, a
// stale baseline entry, or a confirmed-known one), plus one per baseline
// entry that no longer corresponds to anything checked.
func checkAll(lib *manual.Library, baseline map[[2]string]baselineEntry) []finding {
	locales := lib.Locales()
	sort.Strings(locales)

	var findings []finding
	seen := map[[2]string]bool{}
	for _, id := range lib.IDs() {
		enTopic, ok := lib.Topic(manual.FallbackLocale, id)
		if !ok {
			// Every topic id comes from en's own tree (see manual.Load), so
			// this would mean a bug in manual itself, not a drift finding —
			// but fail loudly rather than silently skip.
			findings = append(findings, finding{
				msg:  fmt.Sprintf("guard-help-drift: topic %q has no English original", id),
				fail: true,
			})
			continue
		}
		enSig := computeSignature(enTopic.Markdown)

		for _, locale := range locales {
			if locale == manual.FallbackLocale {
				continue
			}
			topic, ok := lib.Topic(locale, id)
			if !ok || !topic.Translated {
				// No file for this locale/topic at all, or manual.Topic
				// fell back to English — guard-help-topics.sh already
				// fails on that gap. Nothing new for this guard to say.
				continue
			}

			locSig := computeSignature(topic.Markdown)
			key := [2]string{locale, id}
			seen[key] = true
			entry, baselined := baseline[key]

			switch {
			case locSig == enSig:
				if baselined {
					findings = append(findings, finding{
						msg: fmt.Sprintf("guard-help-drift: locale %q topic %q: structure now matches English, but a baseline entry still claims a mismatch (%s) — remove the stale entry from %s",
							locale, id, entry.Signature, baselinePath),
						fail: true,
					})
				}
			case !baselined:
				findings = append(findings, finding{
					msg: fmt.Sprintf("guard-help-drift: locale %q topic %q: structure differs from English (English %s, %s %s) with no baseline entry — fix the translation or add a reviewed entry to %s",
						locale, id, enSig, locale, locSig, baselinePath),
					fail: true,
				})
			case entry.Signature != locSig:
				findings = append(findings, finding{
					msg: fmt.Sprintf("guard-help-drift: locale %q topic %q: drift no longer matches the baseline entry (baseline has %s, actual is %s vs English %s) — update %s deliberately",
						locale, id, entry.Signature, locSig, enSig, baselinePath),
					fail: true,
				})
			case entry.English != enSig:
				// The locale side hasn't moved, but English has — this is
				// exactly the hole the baseline used to leave open: without
				// this check, an entry recorded once stays "known drift"
				// forever even as English grows further ahead, because only
				// the locale signature was ever compared.
				findings = append(findings, finding{
					msg: fmt.Sprintf("guard-help-drift: locale %q topic %q: English moved since this drift was recorded (baseline recorded English %s, English is now %s, %s unchanged at %s) — re-translate or re-record the baseline entry in %s",
						locale, id, entry.English, enSig, locale, locSig, baselinePath),
					fail: true,
				})
			default:
				// Known, recorded drift, anchored on both sides — allowed,
				// but still worth a line so a CI log shows exactly what's
				// being carried forward.
				findings = append(findings, finding{
					msg: fmt.Sprintf("guard-help-drift: locale %q topic %q: known drift (%s vs English %s), tracked: %s", locale, id, locSig, enSig, entry.Reason),
				})
			}
		}
	}

	// A baseline entry naming a locale/topic pair this pass never saw
	// (deleted topic, retranslated-and-removed locale, a typo in the
	// entry) is exactly as stale as one whose signature no longer matches —
	// same "an entry that goes stale fails" rule.
	baselineKeys := make([]string, 0, len(baseline))
	for key := range baseline {
		baselineKeys = append(baselineKeys, key[0]+"/"+key[1])
	}
	sort.Strings(baselineKeys)
	for _, k := range baselineKeys {
		locale, topicID, _ := strings.Cut(k, "/")
		key := [2]string{locale, topicID}
		if !seen[key] {
			findings = append(findings, finding{
				msg: fmt.Sprintf("guard-help-drift: baseline entry for locale %q topic %q does not correspond to any checked topic — remove it from %s",
					key[0], key[1], baselinePath),
				fail: true,
			})
		}
	}

	return findings
}

// refreshBaseline recomputes every EXISTING baseline entry's Signature and
// English fields against the library's current content, dropping an entry
// once it no longer represents a real mismatch (the translation was fixed —
// this is the normal way a baseline entry goes away) or once its
// locale/topic pair no longer exists at all (a deleted topic). It never
// ADDS an entry for drift that isn't already recorded: picking the `reason`
// text for a brand-new exemption is a human/reviewed decision, not
// something to automate.
func refreshBaseline(lib *manual.Library, baseline map[[2]string]baselineEntry) map[[2]string]baselineEntry {
	refreshed := make(map[[2]string]baselineEntry, len(baseline))
	for key, entry := range baseline {
		locale, id := key[0], key[1]
		enTopic, ok := lib.Topic(manual.FallbackLocale, id)
		if !ok {
			continue // topic no longer exists at all
		}
		topic, ok := lib.Topic(locale, id)
		if !ok || !topic.Translated {
			continue // locale/topic pair no longer exists (as a real translation)
		}
		enSig := computeSignature(enTopic.Markdown)
		locSig := computeSignature(topic.Markdown)
		if locSig == enSig {
			continue // fixed — this entry has served its purpose, drop it
		}
		entry.Signature = locSig
		entry.English = enSig
		refreshed[key] = entry
	}
	return refreshed
}

// writeBaseline serializes entries back to path, sorted by locale then
// topic so the diff of a refresh is minimal and reviewable.
func writeBaseline(path string, entries map[[2]string]baselineEntry) error {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k[0]+"/"+k[1])
	}
	sort.Strings(keys)

	bf := baselineFile{Entries: make([]baselineEntry, 0, len(entries))}
	for _, k := range keys {
		locale, topicID, _ := strings.Cut(k, "/")
		bf.Entries = append(bf.Entries, entries[[2]string{locale, topicID}])
	}

	data, err := json.MarshalIndent(bf, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func main() {
	update := flag.Bool("update", false, "refresh already-recorded baseline entries' signatures instead of checking; drops any entry that's now fixed or whose topic no longer exists; never adds a new entry")
	flag.Parse()

	lib, err := manual.Load(uiassets.HelpFS, "help")
	if err != nil {
		fmt.Fprintf(os.Stderr, "guard-help-drift: loading manual: %v\n", err)
		os.Exit(1)
	}

	baseline, err := loadBaseline(baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "guard-help-drift: %v\n", err)
		os.Exit(1)
	}

	if *update {
		refreshed := refreshBaseline(lib, baseline)
		if err := writeBaseline(baselinePath, refreshed); err != nil {
			fmt.Fprintf(os.Stderr, "guard-help-drift: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s: %d entries refreshed (%d dropped as fixed or gone)\n",
			baselinePath, len(refreshed), len(baseline)-len(refreshed))
		return
	}

	fail := false
	for _, f := range checkAll(lib, baseline) {
		if f.fail {
			fmt.Fprintln(os.Stderr, f.msg)
			fail = true
		} else {
			fmt.Println(f.msg)
		}
	}

	if fail {
		os.Exit(1)
	}
	fmt.Println("✓ help-drift guard: every translated topic's structure matches English (or is recorded, current, in the baseline)")
}
