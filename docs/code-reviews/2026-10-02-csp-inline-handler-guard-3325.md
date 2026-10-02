# Review: CSP slice 2 foundation — inline-handler guard and its test (ut-docs#3325)

- **Branch**: `feat/3325-no-inline-handlers-guard`
- **Author**: Opus (pipeline Dev)
- **Reviewer**: Fable, as an independent subagent with a fresh context, a different model from the author.
- **Date**: 2026-10-02

## Scope

ut-docs#3325 turned out to be about three times the designed size (157 real
inline handlers across 39 files, ~10 listener shapes, Go-rendered handlers
outside `web/ui`, templated JS bodies). Dev stopped after the foundation and
the card was re-scoped: the template conversion is now ut-docs#3508, #3509
and #3510. This review covers only that foundation, two new files and
nothing else:

- `scripts/ci/guard-no-inline-handlers.sh`
- `scripts/ci/guard-no-inline-handlers_test.sh`

No Go, no templates, no locale keys, no `ci.yml` change. The guard is
**deliberately not wired into CI** — it reports 157 real violations today
and would redline every build; #3510 wires it once the conversion slices
land. The question here is whether the guard and test are safe to merge as
a standalone foundation, not whether #3325 is done.

## What shipped

- The guard scans every `web/ui/**/*.html` with HTML, Go-template, `/* */`
  and whole-line `//` comments blanked (newlines kept, so reported line
  numbers match the file), reports every `hx-on…` and ` on<event>=`
  attribute as `file:line`, and exempts only an exact `file:line` entry
  (path relative to `web/ui`) in `scripts/ci/inline-handler-allowlist.txt`.
  A whole-file entry never matches. It also checks that
  `web/public/inline-actions.js` still registers its four delegated
  listeners (click, capture-phase error, `htmx:afterRequest`,
  `htmx:beforeRequest`) outside comments.
- Fail-closed: a missing UI dir, a UI dir with no templates, and a missing
  `inline-actions.js` all exit 1. A missing allowlist exempts nothing.
- Positional arguments point it at fixtures, the same convention as
  `guard-autofill-suppression.sh` / `guard-htmx-loaded.sh`; the test harness
  (`expect_pass`/`expect_fail`, `mktemp -d` + trap, real-tree assertion last)
  is the same shape as `guard-autofill-suppression_test.sh`.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker (CI) | `allowed()` piped `grep -v … \| grep -qxF` under `pipefail`. `guard-pipefail-grep-q.sh`, which CI runs over all of `scripts/ci` (`ci.yml` line 91), already failed on the file — merging would have gone red even with the new guard unwired. | **Fixed.** The allowlist is read once into a variable and each lookup greps a here-string. `guard-pipefail-grep-q.sh` now reports ok. |
| 2 | major | False negatives in the match. `[[:space:]]on[a-z]+=` missed an attribute at the start of a line (`<button\nonclick=`), directly after a Go action (`{{ end }}onclick=`, a shape the real tree has at `pages/tax_codes.html:23` with a space today), directly after a closing quote (`class="a"onclick=`), and upper-case `ONCLICK=`. All are valid HTML that `script-src-attr` blocks. The `hx-on` half also matched the bare word in prose. | **Fixed.** Pattern is now `hx-on([:-]\|=)\|(^\|[[:space:]"'}])on[[:alpha:]]+=` with `-i`. `=` must follow the name directly, otherwise a script's `var online = …` (`layouts/base.html:1655`) becomes a false hit. Count on the real tree is unchanged at 157, so nothing new is flagged in real templates. Five test assertions added (four reject cases, one near-miss pass case). |
| 3 | minor | Invariant 2's capture-phase `error` check is per line and only accepts an empty inline function or a named handler before `, true)`. A multi-line function literal in the real `inline-actions.js` would fail the guard even though the delegation is correct. Not fail-open, but a trap for slice 2a. | **Documented** in the guard header: register `error` with a named handler on one line. |
| 4 | minor | A nonexistent allowlist path was handled (nothing exempt) but not tested. | **Fixed.** Assertion added. |
| 5 | minor | The generic `/* … */` blanking runs over the whole template, not just `<script>`/`<style>`. A `/*` in prose or an attribute value, with a later `*/` anywhere in the file, would blank the real handlers between them (silent false negative). Checked the real tree: all 86 files with `/*` have it inside inline JS or Go comments; an unterminated `/*` blanks nothing (fail safe). | **Accepted** as low likelihood; a scoped stripper is a reasonable improvement if it ever bites. |
| 6 | minor (deferred) | No stale-entry detection: an allowlist entry whose line is now clean stays silently; the header's "same-file comment explaining why" rule is not enforced. Line drift itself fails safe — the moved line is re-flagged (tested). `guard-core-neutral.sh` is the precedent for stale-entry + shrink-only checks. | **Deferred to #3510**, the slice that will add the first entries. |
| 7 | nit | `cut -c1-220` is byte-based on GNU and can split a multibyte character (`✕`, `🖨` occur in flagged lines). Cosmetic. | Accepted. |
| 8 | nit | With the guard file absent, the `expect_fail` assertions pass vacuously (bash's "No such file" exits non-zero). The suite still fails through `expect_pass`; same shape as the precedent. | Accepted. |

Also checked and found fine:
- `-->` inside a JS string, an unterminated `<!--`, a `https://` URL on the
  same line as a handler built in a script string, `{{- /* */ -}}` trim
  markers, an indented `// …` line: all behave (comment forms blank
  correctly; the real handler on the next line is still reported with the
  right line number).
- Not flagged, correctly: `data-on-after-request=`, `?only=1` in an href,
  `based on x=1` in prose, `el.onclick = f` inside a `<script>` (an inline
  script is slice 3's concern, not an attribute handler).
- Reported violations come before the `inline-actions.js` existence check,
  so a missing file does not hide the template report.
- Toolchain: `perl -0777`, `find -print0`, `grep -E` — GNU/Linux, same as
  every sibling guard; CI is Ubuntu. No locale-sensitive construct.
- No client or shop name, no secret literal; fixtures are generic.

## TDD and verification

- Revert/restore, run inline: moved `scripts/ci/guard-no-inline-handlers.sh`
  out of the tree, ran the test: every `expect_pass` assertion failed with
  `bash: scripts/ci/guard-no-inline-handlers.sh: No such file or directory`
  and the suite exited non-zero. Restored from `HEAD`, re-ran: 20 fixture
  assertions passed, the one expected real-tree assertion failed. Working
  tree clean afterwards.
- After the fixes: 26 fixture assertions pass; the single failure is the
  real-tree assertion, for the two expected reasons — `157 inline
  handler(s) in web/ui` and `web/public/inline-actions.js does not exist`.
  That is the point of this slice: the guard proves it sees the real
  violations before anything is converted.
- `bash -n` on both scripts; `guard-pipefail-grep-q.sh` ok.
  **Not run:** `shellcheck` — no binary in this container (same gap noted in
  earlier reviews today); CI's `guard-shellcheck-version` job gives the
  verdict on the PR.
- The guard is not referenced in `.github/workflows/ci.yml`, confirmed by
  grep, so this branch adds no new CI job.

## Verdict

**Safe to merge as a standalone foundation.** The guard follows the repo's
guard conventions, fails closed on every missing input, keeps line numbers
exact for the allowlist, and (after finding 2) catches every attribute
shape I could construct without flagging anything new in the real tree.
Finding 1 would have broken CI and is fixed. This PR does not close
ut-docs#3325; the conversion stays open, tracked on ut-docs#3508/#3509/#3510,
and #3510 is where the allowlist hygiene (finding 6) and the CI wiring
belong.
