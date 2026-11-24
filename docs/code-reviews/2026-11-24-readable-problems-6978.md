# Review record: readable grouped problems on /backoffice (ut-docs#6978)

Scope: new `internal/problemclass` (classifier: area, severity, known error, reason and next-step keys);
`internal/pages/backoffice_problems.go` (grouping, collapse of repeats, worst status, copy-for-support text);
`backoffice_page.go` (no 5-line cap, heartbeat age cut); `web/ui/pages/backoffice.html` "Recent problems" card;
`web/public/app.css` (`.tag.danger`, card styles, logical properties); 45 new `backoffice.problems.*` keys in
`en.json` + `en.meta.json` (English only, ADR-0160); `backoffice.no_problems` removed (and its baseline line);
help topic `web/help/en/alerts.md` + regenerated docs-shots; `internal/data/setting_scope_test.go` lists the
classifier's key-prefix constant as not a settings key.

Phases: BA, Architect (Opus), UX, Dev (Sonnet), Tester, Reviewer (Opus, independent, read-only, a different
model from the author), one fix round (Sonnet), scoped Tester re-check of the fixes.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | medium | `sync pull:`/`sync push:` (LAN main-till link) classified as Cloud sync; `sync push:` fell to Other | fixed: those prefixes, the `sync.` key prefix and `builtin locale bundle:` go to Network; TestClassify_AreaByPrefix pins it (reverting fails it) |
| M2 | medium | Unkeyed problems never cleared on the page (no age cut), so every passing warning stayed with "if this stays here" advice; help said "disappears once fixed" | fixed: the page uses `logging.OpenProblems(clock.Now(), problemReportMaxAge)`, the heartbeat's cut; TestPageProblems_AgeCutHitsOnlyUnkeyed (reverting to `logging.Recent()` fails it); help corrected |
| L1 | low | Time not localized (English month, Latin digits) | fixed: `httpx.FormatDateTime` + `LocalizeDigits`, tested with fa digits |
| L2 | low | `time.Now` instead of `clock.Now` (docs-shots determinism) | fixed |
| L3 | low | Startup line (data dir, log path) in the copy text reached anyone with `reports` and demo visitors | fixed: included only with the `settings` permission and not in demo mode; help notes it (no dedicated test, accepted) |
| L4 | low | No test that dynamic `area.*`/`severity.*` keys exist in en.json | fixed: key-existence test covers them |
| L5 | low | Checksum reason named "plugin or update" for catalogue-image lines; selfupdate lines in Other | fixed: neutral reason; selfupdate/android update lines and keys under Plugins |
| L6 | low | `not_main_till` advice "Replace this till" misleads on a till that is rightly not main | fixed: conditional wording (key unshipped, same key) |
| T1 | low (Tester) | "Show the text for support" summary 23–27px; row summaries lost the disclosure marker | fixed: 44px min height, visible chevrons; re-checked by Tester |
| T2 | low (Tester) | Unclassified rows looked identical until opened | fixed: escaped 80-rune snippet in the summary, tested |
| T3 | test gap (Tester) | 3 mutants survived (support block repeated row content; cloudsync prefix untested) | fixed by Tester in tests; 16/16 mutants now fail a test |
| T4 | low (Tester re-check) | Closed disclosure chevron did not mirror in RTL (▸ pointed away from the text); "Show technical details:" squeezed next to the snippet at 360px | fixed [no-subagent: two-line CSS fix]: `html[dir="rtl"]` closed state uses ◂; summaries `flex-wrap: wrap`; computed `::before` content checked in Chromium for ltr/rtl; docs-shots regenerated |
| T5 | test gap (Tester re-check) | The hidden case of the startup line (no `settings` permission, or demo) has no test | fixed: TestBackofficePage_StartupLineOnlyOutsideDemo (removing the demo gate fails it, re-verified) |
| C1 | CI (`locale-render-audit`) | New area title "Other" equals existing de-translated keys' text, so the PR carve-out for new keys does not apply and `/backoffice?lang=de` failed; "Plugins", "Payments", "Cloud sync", "Failed", "Till" collide the same way | fixed: unique, more precise wording (Other problems; Plugins and updates; Payments and card readers; Sync with Universal Till Cloud; Not working; Till details), each checked to share no value in en.json; help + docs-shots updated |
| C2 | CI (`desktop-shell` deadcode baseline) | `problemclass.AllKeys` reachable only from tests | fixed: moved to `allkeys_test.go`; every other new func checked to have a non-test caller |
| N | nit | unused fields/CSS, `unicode-bidi: plaintext` on the support pre, help keywords | fixed |

Confirmed OK by the reviewer: html/template escaping of raw lines; problem lines are redacted in
`logging` before entering the ring; the ring is copied under its mutex; the existing `[data-copy-target]`
handler has an offline fallback; `backoffice.no_problems` stays only as a Weblate orphan in ar/fa/tr (warning
only; translations are never edited in a code PR).

## Verification

- gofmt, `go build ./...`, `go vet`, golangci-lint (0 issues); `go test` problemclass, pages, data; `-race` on
  problemclass and pages `-run 'Problem|Backoffice'` (full `internal/pages -race` left to CI: 90+ min on this
  4-core container). Tester ran all build-job guards; docs-shots regenerated and `run-docs-shots-guard.sh` passes.
- Driven run (real app, warnings logged after boot): screenshots at 1024x600, 360px, desktop, fa RTL and the
  empty state were looked at — fixed area order, worst-severity tag, words not colour alone, repeats collapsed
  ("3 times"), a 600-char line complete and wrapped, no horizontal scroll, Copy → "Copied" with correct clipboard
  text, keyboard focus visible, RTL mirrored with raw text left-to-right. Not on touch hardware or kiosk mode.

Pre-flight: walked reviewer/PREFLIGHT.md — help topic `web/help/en/alerts.md` updated + `make docs-shots`
updated; en.meta.json updated (English only); `reference/` n/a (no reference page describes the back-office
problems card); README n/a (not stale); cross-repo follow-ups (my. Tills wording, heartbeat digest contract,
status-bar chips, Settings → Diagnostics copy format, keyed/typed problem codes) listed on ut-docs#6978.
TDD re-verified by the orchestrator for M1 and M2 (revert → named test fails → restore → passes).

Verdict: ship (after the fix round).
