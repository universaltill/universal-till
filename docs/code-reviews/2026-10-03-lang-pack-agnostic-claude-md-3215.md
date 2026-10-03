# Review: language-pack rule no longer hard-codes de/es (ut-docs#3215)

**Date:** 2026-10-03
**Branch:** `fix/3215-lang-pack-agnostic-claude-md` (commit `0def77a`, parent `109d60c` = `main`)
**Scope:** `CLAUDE.md` only, prose. No code, tests or runtime behaviour.
**Reviewer:** independent session (Opus 5.5), not the author (Sonnet 5).

## What shipped

In the "API, formats, i18n" section of `CLAUDE.md`, this sentence:

> A new `en.json` key needs follow-up PRs in `ut-plugin-language-{de,es}`.

now reads:

> A new `en.json` key needs follow-up PRs in every `ut-plugin-language-*`
> pack `scripts/ci/check-lang-pack-drift.sh` checks — see that script's
> `PACKS` array for the current list; never hard-code pack names here.

The old wording was stale. `check-lang-pack-drift.sh` already checks a
third pack, `ut-plugin-language-pt`.

## What was checked

- **The diff is exactly one paragraph.** The commit touches 1 file
  (+3/−1). The surrounding `lang-pack-drift.yml` lines are unchanged and
  still read correctly after the new sentence.
- **The claim is true.** On `main`, `scripts/ci/check-lang-pack-drift.sh`
  has a bash array literally named `PACKS`. It holds
  `ut-plugin-language-de:de`, `-es:es` and `-pt:pt`, and the script loops
  over it. A reader who follows the pointer gets the real current list.
- **Other live copies of the stale list:** I searched both repos for
  `ut-plugin-language-{de,es}`. Almost every hit is a dated review record or
  ADR, which are historical and stay as written. The live hits are below.
- **Same-session doc updates.** This changes a rule's wording, not
  behaviour, so no help topic, README or reference needs to change with it.
- **CI.** No `paths:`-scoped guard covers `CLAUDE.md`, so only the
  unscoped workflows (`ci.yml`, `e2e.yml`, `commit-attribution.yml`) should
  run.

## Findings

| # | Severity | Finding | Disposition |
|---|---|---|---|
| 1 | Low | ut-docs `architecture/plugin-architecture.md` §8 sample table lists `ut-plugin-language-{de,es}` (pt missing). ut-docs `reference/list-and-dialog-pattern.md` checklist says "the `ut-plugin-language-{de,es}` packs". | Deferred. These are in a different repo and outside this card, which BA scoped to `CLAUDE.md`. Needs its own Backlog card. |
| 2 | Nit | `check-lang-pack-drift.sh`'s header comment still says "Adding a third pack later is a one-line edit" and "the OTHER pack". There are already three packs. | Deferred to the same follow-up. The comment does not affect what the script does. |
| 3 | Nit | A comment in `internal/httpx/httpx.go` mentions `ut-plugin-language-{de,es}`. It explains why a past change reused a key. | Accepted as historical rationale. No change needed. |

The `CLAUDE.md` wording itself has no defects, so I made no fix commit.

## Verdict

**Safe to merge.** The change is one accurate, self-contained sentence. It
points at the list the CI actually checks, so it will not go stale again
when a new pack is added. Findings 1–2 are deferred to a follow-up card.
