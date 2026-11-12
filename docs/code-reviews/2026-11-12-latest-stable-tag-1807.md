# Review: every "latest release tag" lookup ignores pre-release tags (ut-docs#1807, slice 1)

**Date:** 2026-11-12 · **Branches:** `fix/1807-latest-stable-tag` (universal-till), `docs/1807-latest-stable-tag` (ut-docs)
**Author model:** Sonnet (Dev) · **Reviewer:** _pending_

## What shipped

Slice 1 of the beta release channel: a `vX.Y.Z-beta.N` tag can never be
mistaken for "the latest release". Nothing publishes betas yet
(`guard-release-notes.sh` still refuses suffixed tags); publishing is the
follow-up card.

- New `scripts/ci/latest-stable-tag.sh [--next major|minor|patch] [DIR]`, the
  one home for "latest stable tag" and the bump arithmetic. Stable means
  exactly `^v[0-9]+\.[0-9]+\.[0-9]+$`; any other `v*` tag is ignored. Order is
  numeric and portable (no `sort -V`). No stable tag prints nothing and exits 0
  (`--next` then starts from `0.0.0`). A `DIR` that is not a repo exits
  non-zero. Bad usage exits 2.
- Callers switched to it: `release.yml` dispatch auto-bump (`BUMP` passed via
  `env:`), `scripts/release.sh`, `nightly.yml` (the inline grep/sort is gone;
  its `X.Y.Z` check stays as a tripwire).
- `ios-testflight.yml`: the `git describe` fallback adds `--exclude 'v*-*'`.
- `ci.yml` build job runs the new self-test.
- `RELEASING.md`: one sentence on the bump base.
- ut-docs `.claude/skills/devops/SKILL.md`: Core release cadence steps 1 and 4
  call the helper (cadence base and next version).

Not in this slice (follow-up card): publishing betas, goreleaser `prerelease`,
`--latest` gating, platform gating, client-side `prerelease` checks.

## Tests

- New `scripts/ci/latest-stable-tag_test.sh` (34 checks, throwaway repos in
  `mktemp -d`): betas ignored with and without a later stable, numeric order,
  noise tags ignored, no tags, only pre-releases, non-repo fails closed, bad
  usage exits 2, plus wiring greps on the callers and `ci.yml`.
- Written first and run red (`FAIL: .../latest-stable-tag.sh does not exist`),
  then green after the helper.
- Driven by hand against the real remote tag list (141 `v*` tags): latest
  `v0.36.0`, `--next patch` `0.36.1`. With a planted `v99.0.0-beta.1` the
  result is unchanged; the old `sort -V` lookup returned the beta.

## Findings

Independent review: Opus 5.5 (author Sonnet 5.5), fresh context. It re-ran the
suite (32/32), shellcheck and the sandbox/fixed-date/mail guards, broke the
helper five ways (each turned the suite red), checked fail-closed on a git
error through every caller, ran the devops skill commands from the ecosystem
root, and checked BSD/bash-3.2 portability. No blocker, no should-fix. Nits:

1. Check count in this record said 28 — fixed (34 after nit 2's cases).
2. `latest-stable-tag.sh`: a leading-zero tag (`v0.1.08`) passed the stable
   pattern, then the bump failed on bash octal — fixed with `10#`, test case
   4d added (red before: `value too great for base`).
3. `release.yml` still interpolates `${{ inputs.version }}` into the script
   (predates this change; the value is then shape-checked). Left as is; noted
   on follow-up #6115.
4. The devops cadence base moves from `--sort=-creatordate` (newest created)
   to the highest stable version — intended: it is the same base
   `release.yml` bumps from, and creatordate mixes tag dates (annotated) with
   commit dates (lightweight).

Tester (separate agent): 16 mutants, 3 survived; two were real test gaps
(minor/major keeping the patch; dropped `^` anchor), both closed with cases
4b/4c. Tester note O1 (no local tags → empty `last`, silent count of 0) is
fixed in the devops skill: fetch tags first, stop on an empty `last`.

**Pre-flight:**
- Till user-visible behaviour / `web/help/`: n/a (release tooling only).
- docs-shots: n/a.
- reference: n/a (`RELEASING.md` updated; nothing in ut-docs `reference/`
  describes tag lookup).
- README: n/a (does not describe tag lookup).
- `en.json`: n/a (no keys).
- ADR: n/a (bug fix following the `nightly.yml` precedent).
- skill-evals: `.claude/skills/**` edited in ut-docs; the orchestrator runs
  `scripts/skill-evals.py`.
- Follow-up: beta channel publishing card (design §8).

## Verdict

APPROVE after nits 1, 2 and 4 were folded in; nit 3 deferred to #6115.
