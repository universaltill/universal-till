# Code review: theme-overridable border tokens for semantic notice/block borders

**Card:** universaltill/ut-docs#2207
**Branch:** `fix/2207-notice-border-colors-theme-tokens`
**Complexity:** easy — Dev inline (Sonnet), review at Sonnet (independent
fresh-context subagent, no shared reasoning with the implementer)

## What shipped

Follow-up from `universaltill/ut-docs#2192` (`universaltill/universal-till#1142`),
which fixed the same class of gap for tint **backgrounds** but explicitly
deferred the **border** half. Four `web/public/app.css` rules still
painted a semantic border color as a literal `rgba(...)` at `.35` alpha,
so Monochrome (or any future theme) couldn't reach them — a Monochrome
notice had a colorless background wash (fixed by #2192) but still a
colored outline.

- Added `--success-tint-border` / `--danger-tint-border` /
  `--warning-tint-border` to `app.css`'s base `:root`, defaulting to the
  exact literal values every affected rule already used
  (`rgba(22, 163, 74, .35)` / `rgba(220, 38, 38, .35)` /
  `rgba(217, 119, 6, .35)`).
- Pointed the 4 rules the card named at the matching token instead of the
  literal: `.pos-notice.success`, `.pos-notice.error`,
  `.notice-block-warn`, `.notice-block-success`. (`.tag.warn`,
  `.journal-replica-notice`, `tr.row-warn td`, `.sync-banner` — the other
  4 rules #2192 touched — don't declare their own border, so there was
  nothing to fix there; confirmed by the independent review's own grep,
  not just re-stated from the card.)
- `monochrome.css` sets all three new tokens to a neutral grayscale
  border (`rgba(0, 0, 0, .35)`, same alpha as before) — mirroring the
  existing `--success-tint`/`--danger-tint`/`--warning-tint` background
  override already in that file.
- `dark.css`: **no change** — it never overrode a border color here (the
  border was always a hardcoded literal, independent of `--danger`), so
  leaving the new tokens unset there is byte-for-byte identical to what
  it rendered before this diff.

New regression test: `TestSemanticTintBorders_MonochromeIsColourless`
(`internal/pages/themes_test.go`) — mirrors the sibling background test,
reads the real embedded `app.css`/theme CSS via `registerStatic`/
`registerThemes`, asserts none of the 4 rules still paints a hardcoded
border, asserts monochrome's three border tokens are grayscale, and
asserts every other built-in theme (amber/fresh/monarch/slate/dark) does
**not** define the new tokens. Also corrected the sibling background
test's now-stale comment claiming borders were "out of scope."

## Independent review — findings

Spawned a fresh-context Sonnet subagent (`complexity:easy` routing) in an
isolated git worktree, with instructions to read the diff cold, run the
full gate itself, independently re-verify the TDD claim, re-derive the
literal-value inventory from a repo-wide grep (not trust the card's own
list), check all 6 theme files (not just monochrome.css), and check the
`dark.css` interaction explicitly.

**Verdict: safe to merge, no code/test defects found.**

| # | Severity | Finding | Resolution |
|---|---|---|---|
| 1 | Process (not a code defect) | This review record didn't exist yet at the time the independent review ran. | This file. Written and committed before merge, per `CLAUDE.md`'s gate. |

The independent review re-derived the literal-rgba inventory itself
(grepping `rgba(22, 163, 74`/`rgba(220, 38, 38`/`rgba(217, 119, 6` across
the whole repo) and confirmed the 4 border sites named by the card were
exhaustive — no fifth hardcoded border site was missed, and the only
remaining literal hits are the 6 `--*-tint`/`--*-tint-border` default
declarations themselves (correctly still literal, by design) and this
review record's own prose.

## Verified beyond automated tests

- **TDD claim independently re-verified** by the reviewer, in its own
  isolated worktree: reverted just `app.css`/`monochrome.css` to
  `HEAD~1`, re-ran the new test — failed with 12 real assertion errors
  naming the missing tokens/literal-still-present sites, not a compile
  error. Restored, confirmed clean, re-ran — passed. (Independently
  reproduced by the implementer beforehand too, via `git stash`.)
- Full `go build ./...`, `go vet ./...`, `gofmt -l .` — clean (both
  passes).
- Full `go test ./...` (whole repo, not just `internal/pages`) — all
  packages green, no regressions elsewhere.
- `golangci-lint run ./internal/pages/...` — 0 issues.
- All 6 theme files checked individually: only `monochrome.css` defines
  the 3 new border tokens; `amber`/`fresh`/`monarch`/`slate`/`dark`
  define none of them, confirming they genuinely inherit `app.css`'s
  literal default (pixel-identical to before) rather than merely looking
  unchanged by coincidence.
- `dark.css` interaction checked explicitly: it already overrides
  `--danger` (ink, for contrast, per its own #2176 comment) but never
  touched a border color here — the border was always independent of
  `--danger`. Since the new token's default equals the exact prior
  literal, `dark.css`'s rendering of `.pos-notice.error`'s border is
  provably byte-identical to before this diff.
- Naming checked against `reference/coding-standards.md` (`ut-docs`) —
  no CSS custom-property naming section exists there to contradict; the
  `-border` suffix on the existing `--*-tint` convention is consistent.
- No `web/locales/*.json`, `web/help/**`, or `web/ui/**` template files
  touched — pure CSS custom properties plus one Go test file, so no i18n
  key, no manual topic, no screenshot content change is owed. Guards run
  and green: `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh` (pre-existing fa/tr
  `vouchers` drift, tracked separately on ut-docs#1973, unrelated to this
  diff), `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-e2e-fixtures-import.sh`.
- `guard-docs-shots.sh`: failed freshness on the raw literal file hash
  (this diff touches `web/public/app.css`, in the guarded surface
  fileset). Same reasoning as #2192's own review applies unchanged:
  `e2e/tests-docs/docs-shots.spec.ts` never switches themes, so every
  routed topic/locale is captured under the seeded default theme
  (`monarch`), where every new token resolves to its unchanged literal
  default — Monochrome, the only theme whose rendering visibly changes,
  appears in zero manual screenshots. Used the documented escape hatch
  (`scripts/ci/update-docs-shots-surface-hash.sh`, `web/help/img/
  manifest.json` diff touches only the `surface_sha256` field, verified)
  with a `Docs-Shots-Unchanged: true` commit trailer, rather than a full
  regeneration.
- No e2e Playwright run in this sandbox (no npm deps installed) —
  defensible for the same reason #2192's review established: a repo-wide
  grep of `e2e/` for the affected selectors/literal values and the new
  token names returns zero hits, and the diff touches zero markup/JS/
  class-lists.

## Deferred / follow-up candidates

None — the card's own scope (the 4 border sites) is exhaustive; nothing
found needing a separate follow-up.

## Verdict

**Safe to merge.** No correctness, security, i18n, money, offline-first,
or data-access issues found by either pass. The only gap the independent
review found — this review record's absence — is closed by this file.

**Not merging automatically this cycle**, same as several sibling PRs
opened the same day: `ut-docs#2277` (Admin Review, unresolved) is asking
whether this pipeline's standing auto-push authorization ("no real users
yet") still holds given evidence of a live till fleet. This PR is pushed
and open for CI (reversible, no live effect either way) but deliberately
held unmerged until a human answers that card.
