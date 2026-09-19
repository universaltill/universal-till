# Code review: auto-delete an orphaned release tag on failure (ut-docs#2274)

**Date:** 2026-09-19
**Card:** ut-docs#2274 — Release pipeline: prepare tags before the build, so
any late failure/cancel leaves a phantom tag (happened 3x: v0.14.21, v0.15.0,
v0.17.0)
**Complexity:** easy — Sonnet built, Sonnet reviewed (fresh context)

## What shipped

`.github/workflows/release.yml`'s `prepare` job creates and pushes the git
tag before any build job runs (every downstream job checks out
`ref: ${{ needs.prepare.outputs.tag }}`), so a build failure or a manually
cancelled run after that point permanently orphans the version: the tag is
real, but nothing was ever published under it, and the next auto-bump reads
it as "latest" and skips past a version nobody ever shipped. Happened for
real three times (v0.14.21, v0.15.0, v0.17.0), each found and cleaned up by
hand days later per the card's own history (see #2274's comments).

**Design choice, and why not the originally-proposed fix.** The card's own
text suggested reordering `prepare` so the tag is pushed only after the
build/verify jobs succeed (in `publish-release` instead of `prepare`),
noting goreleaser would then need `--skip=validate`/`GORELEASER_CURRENT_TAG`
rather than an existing tag. Scoping that during BA/Architect found it would
require rewiring every downstream job's `ref: needs.prepare.outputs.tag`
checkout to a commit SHA instead, plus restructuring goreleaser's own
tag-based versioning — a materially larger, architectural change to a
pipeline that ships real cross-platform builds (macOS notarization, Android
signing, Windows NSIS), that this session has no way to verify end-to-end
(no local capability to run a real multi-platform release). Given the
actual acceptance goal is "no permanently-burned version numbers," not
specifically "tag after build," a functionally-equivalent, much
lower-risk fix was chosen instead: added `cleanup-orphaned-tag`, a final
job that deletes the tag (and any draft release goreleaser already
created) whenever a `workflow_dispatch` run does not reach
`publish-release` successfully. Zero jobs' checkout refs changed; zero
change to goreleaser's versioning. Scoped to `workflow_dispatch` only — a
tag a human pushes manually (`git tag && git push`, or `scripts/release.sh`)
is explicit intent and is never touched by this job.

The full reorder remains available as future work if the team still wants
it; not filed as a new card since it's optional/deferred, not dropped
scope — the acceptance criterion (no orphaned tags surviving a failed run)
is met by this fix.

## Independent review (fresh-context Sonnet)

Traced the `needs`/`if` graph by hand through every job for each of: a
mid-build failure (`linux-shells`/`goreleaser` fails), a job timeout, a
whole-run cancellation, `prepare` itself failing (no tag ever created), and
the `push`-triggered manual path. Confirmed `needs.publish-release.result`
(hyphenated job id) is valid syntax against existing precedent in the same
file (`publish-release` already reads `needs.windows-installer.result`
etc.), confirmed `always()` (not `!cancelled()`) is the deliberate, correct
choice for a job that must also run after cancellation, confirmed no race
with still-running build jobs (the job's own `needs:` list names every
other job, so GitHub guarantees they've all reached a terminal state
first), and confirmed the shell script's quoting/fallback logic is safe.

**Verdict: approved, no blocking findings.** Three non-blocking notes,
addressed as follows:
1. A sub-second window inside `prepare` between the tag push and its
   `GITHUB_OUTPUT` write could leave `prepare.result != success` despite a
   real tag existing — negligible; doesn't match any of the three real
   incidents (all failed well after `prepare` completed). Accepted as-is.
2. Cancelling during `publish-release` itself (everything already built)
   would delete a fully-built draft release rather than let a retry flip
   it live. Accepted deliberately and now documented inline in the
   job's own comment — that window is one API call wide, versus the three
   real incidents this job exists to prevent.
3. Both cleanup commands swallow their own failures
   (`2>/dev/null || true`) by design ("must never fail this job"), so a
   genuine API/permissions fault would look identical to "nothing to
   clean up." Accepted — this is a best-effort cleanup step for an
   already-failed run, not a load-bearing guarantee.

## Verified beyond automated tests

No workflow-testing tooling exists in this repo (no `actionlint`/
`yamllint` in CI, confirmed by searching `scripts/ci/`) and there is no
safe way for a cold cloud cycle to dispatch a real multi-platform release
to prove this end-to-end. Verified instead: `python3 -c "import yaml;
yaml.safe_load(...)"` parses the full file without error before and after
every edit; the job graph and GitHub Actions expression semantics were
traced by hand (see above) rather than assumed; `scripts/release.sh` was
re-read to confirm the manual path is unaffected. **This fix is unproven
against a real release run** — same honest caveat this card's own history
already carries for ut-docs#2257's android `setup-android` fix (also
shipped and left "unproven in a real release" pending the next dispatch).
DevOps follow-up: watch the next real `workflow_dispatch` release (success
or a deliberately-forced failure) and confirm `cleanup-orphaned-tag`
behaves as traced above.

## Safe-to-merge verdict

Safe to merge. Purely additive (one new job, 67 lines), no existing job's
behavior changed, independently reviewed with no blocking findings.
