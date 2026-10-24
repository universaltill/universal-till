# Code review — phone card labels never break mid-word (ut-docs#3881)

**Date:** 2026-10-24 · **Branch:** `fix/3881-t-cards-label-wrap` · **Lane:** `lane:cloud-41`
**Author model:** Opus · **Reviewer:** Sonnet (independent subagent, read-only)

## What shipped

- `web/public/app.css`: the phone-tier (`≤480px`) card label
  (`table.t-cards tr > [data-label]:not(.t-cards-title)::before`) now sets
  `overflow-wrap: normal; hyphens: auto; min-inline-size: min-content`.
  It used to inherit the cell's `overflow-wrap: anywhere` (meant for data),
  so a long one-word header broke between any two letters ("GESAMTBETR" /
  "AG"). A label now breaks only between words or at a hyphenation point of
  the page language; where it can't, its column grows past 40% to fit the
  word. Short and multi-word labels keep the 40% column as before.
- `e2e/tests/phone-cards-label-wrap-3881.spec.ts`: injects a card-mode
  table (de, and fa/RTL) at 360px and compares each rendered `::before`
  label with a reference probe (same font, language and `hyphens`, but no
  break-anywhere): same number of lines, and never narrower than its
  min-content. Works with or without a hyphenation dictionary.
- `web/help/img/manifest.json`: surface hash only. Every manual
  screenshot is 1024×600 and the rule is inside `@media (max-width:480px)`,
  so no manual pixel changes (`make docs-shots` in this container rewrote
  125 PNGs from font drift alone; those were discarded).

## TDD

Spec written first. Without the fix: both tests fail
(`Mehrwertsteuerbetrag` label 115px wide, 2 lines vs. reference 1 line;
min-content 186px). With the fix: 2 passed. Re-verified revert → red →
restore → green in one command.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | A label whose single unbreakable piece is wider than the whole card (~35+ uppercase characters at 360px) now overflows the card, where before it split mid-word inside it. `min-width` beats `max-width`; the suggested `min(min-content, 100%)` is not valid CSS (intrinsic keywords are not allowed in math functions). | Accepted — no real header in en/de/fa/tr/ar is that long; a broken word was the worse outcome the card asked to remove. |
| 2 | Low | Without a hyphenation dictionary a ~20-char German word grows the column to ~160 of ~300px; the value sits beside or under it. | Accepted — readable, matches the AC's "or the label column grows". |
| 3 | Low | The fa test does not cover a label wider than the card. | Accepted with #1. |

## Verified beyond automated tests

Screenshots taken at 360px and looked at: German injected table
(`GESAMTBETRAG` whole, `MEHRWERT-/STEUERBETRAG` hyphenated, multi-word
label wraps at spaces, values aligned); Persian (RTL) injected table (long
ZWNJ-joined word on one line, column grows, right-aligned), light and dark
colour scheme (identical — the till theme does not follow
`prefers-color-scheme`; no colour changed here). Not checked: 1024×600
(rule does not apply above 480px), real phone hardware (emulated mobile
Chromium only), WebKit.

## Gate

`go build ./...`, `go test ./...`, every guard in `ci.yml`'s build job
(except `guard-shellcheck-version.sh`/`shellcheck`: not installed in this
container; no shell script changed), the new spec. No Go change, so
`golangci-lint` not re-run.

**Verdict:** safe to merge.
