# Review: no-showmodal guard stops hiding code after a non-comment `/*` (ut-docs#6132)

Date: 2026-11-12 · Lane: `lane:cloud-41` · Author model: Opus · Reviewer: independent Sonnet subagent (also ran the Tester checks for this guard-only change)

## What shipped

- `scripts/ci/guard-no-showmodal.sh`: `strip()` now opens a block comment only
  for a `/*` at line start, after whitespace or as `{{/*` (`blockopen()`), and
  ends the code at a `//` comment before any `/*` or `<!--` after it
  (`linecomment()`). Ported from the `guard-no-native-confirm.sh` fix in
  universal-till#2514 (ut-docs#6116), inline rather than shared, so neither PR
  conflicts with the other. Header comment updated.
- `scripts/ci/guard-no-showmodal_test.sh`: four new cases — a call after a
  `//` comment containing `/*`, after `accept="image/*"`, after `"a/*"` on the
  same line (all must fail), and real comments next to a URL (must pass).

## Evidence

- TDD: with `origin/main`'s guard the three new must-fail cases fail
  ("3 guard-no-showmodal test(s) failed"); with the fix all pass. Re-verified
  independently by the reviewer in a separate worktree.
- Real tree: the fixed guard now scans 558 more real code lines than before
  (`web/public/app.js` +385, `bluetooth_devices.html` +120,
  `self_order_shop.html` +46, `categories.html` +4, `catalog.html` +3) and finds
  no `showModal()` call in them — the guard stays green, nothing to fix or mark.
- `shellcheck` clean on both files; `guard-ci-tests-sandboxed.sh` green; runs
  under mawk 1.3.4 (Ubuntu default).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | Test header line wrapped at >100 chars | fixed |

Reviewer's adversarial fixtures (`x;/* c */ d.showModal();`, `{{- /* c */ -}}`,
`<script>/* c */`, tab indent, `url="http://x/*"`, `x = a / *b`, …) were all
caught. Only remaining miss is the already-documented ` /*` inside a string
(not a regression). A glued `/* … */` is no longer stripped: at worst a false
alarm on its prose, never a hidden call — documented in the header.

Pre-flight: review record — updated; help/README/reference — n/a (CI guard internals, no user-visible or documented behaviour change; CLAUDE.md's guard rule is unchanged); en.json/locales — n/a; follow-ups — none.

## Verdict

Approve.
