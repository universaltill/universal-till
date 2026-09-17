# Code review: bound the `goreleaser` release job with a timeout (ut-docs#2274)

## What shipped

`.github/workflows/release.yml`'s `goreleaser` job gained
`timeout-minutes: 40`. That job hung indefinitely in its "Run tests
before releasing" step on 2026-09-16 (run #202, `v0.15.0`'s `prepare`
job had already tagged before the hang) — zero step-timestamp movement
for hours against a historical baseline of ~6.5m for that step alone —
occupying a runner silently instead of failing loudly, per ut-docs#2274
Finding 2.

40m is generous headroom over the ~6.5m step baseline plus the
`internal/plugins` package's own `-timeout 20m` sub-budget
(ut-docs#643/#753/#776) and goreleaser's cross-platform build/upload,
while still bounding a genuine hang to well under GitHub's 360m job
default.

## What this PR deliberately does NOT do

ut-docs#2274 also raised two other findings, both intentionally left
out of this diff:

- **The `v0.14.21`/`v0.15.0` phantom tags** (Finding 1 and the tag
  `prepare` created just before the Finding 2 hang) are left as-is —
  neither deleted nor otherwise touched. Deleting a pushed release tag
  is a destructive, hard-to-reverse action on shared repo state; the
  card itself offers "leave it as known debris and document it" as an
  equally valid alternative, and that's the one taken here. This
  comment is the documentation: both tags are known, expected debris
  from failed/hung runs, not evidence of a further bug.
- **Reordering `prepare` to tag after builds succeed** (also suggested
  in Finding 1) is a real design change to the release workflow's own
  tagging semantics (what `goreleaser`'s changelog/version derivation
  reads, what a re-dispatch after a partial failure looks like) — out of
  scope for a same-cycle infra fix; worth its own card if wanted.

## Independent review

Reviewed inline (single-line, well-scoped YAML addition; the change has
no code path to unit-test — GitHub Actions' own timeout enforcement is
the mechanism, not application logic). Checked:

- YAML parses (`python3 -c 'import yaml; yaml.safe_load(...)'` — OK).
- 40m does not undercut any known-good run: the step-level `-timeout
  20m` inside `internal/plugins`' own test run, plus the ~6.5m baseline
  for the full "Run tests before releasing" step, plus goreleaser's own
  build/upload time (observed a few minutes in prior successful runs),
  comfortably fit inside 40m with real margin — this is a ceiling on a
  *hang*, not a tightening of the normal-case budget.
- No other job in this workflow shares the same starvation risk without
  its own review: `windows-installer`/`macos-app`/`android-app`/
  `verify-versions` all poll a *bounded* release-visibility loop (`for i
  in $(seq 1 30); ... sleep 20` = 10m hard cap) rather than a single
  unbounded command, so they were not silently exposed to the same
  failure mode. Not adding `timeout-minutes` to those in this PR — each
  already fails loudly on its own bound; a repo-wide sweep for jobs
  *without* any such bound is worth its own card if wanted, per the
  card's own "ideally to the other long jobs" phrasing (advisory, not
  required for this fix).

## Verified beyond automated tests

Could not reproduce the hang itself (it's an unexplained CI-runner
stall, not a deterministic code path) — verified the mechanism instead:
GitHub Actions' `timeout-minutes` at the job level is a platform-enforced
wall-clock cap independent of what the job's steps are doing, so it
covers a hang in *any* step of this job (not just the one that hung this
time), including one with no internal timeout of its own (goreleaser's
own `release --clean` invocation has none).

## Safe-to-merge verdict

Safe to merge: minimal, additive, no behavior change on any run that
currently completes within 40 minutes. **Holding the merge for human
sign-off, not merging automatically** — see the PR description for why
(the auto-push/auto-release standing authorization in `devops`'s
SKILL.md §7 is explicitly scoped to "no real users yet," and this same
card's own text — "the fleet received nothing... across 9 feat/fix
merges" — is direct evidence a real till fleet is now in production,
which is exactly the condition that scoping says should revert release
autonomy to human-gated).

## Explicitly deferred

- Deleting/cleaning up the `v0.14.21`/`v0.15.0` phantom tags — left as
  documented debris (see above).
- Reordering `prepare`'s tag-then-build sequencing.
- Auditing every other workflow in this repo for the same
  no-internal-bound gap this job had.
- Re-confirming whether the pipeline's auto-push/auto-release
  authorization should be formally revoked given the live-fleet evidence
  above — flagged separately on the board (see PR description), not
  decided in this review.
