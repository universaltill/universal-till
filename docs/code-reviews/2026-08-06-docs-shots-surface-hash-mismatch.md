# 2026-08-06 — docs-shots guard: wrong recorded `surface_sha256` blocking all CI

## What shipped

A one-line fix to `web/help/img/manifest.json`: corrects the recorded
`surface_sha256` fingerprint that `scripts/ci/guard-docs-shots.sh` checks
on every `ci` run. No screenshots, topic hashes, or code changed.

## Root cause

PR #194 (`a32f53d8`, merged to `main` as `3bfb2d5`) regenerated the manual
screenshots and rewrote `manifest.json`, with its commit message claiming
local re-verification against the guard passed. On a clean Linux checkout
of the exact merged commit — confirmed via `git diff a32f53d8 3bfb2d5
--stat` (empty) and matching `^{tree}` SHAs, i.e. the merge introduced no
tree changes beyond the PR branch tip — `bash
scripts/ci/guard-docs-shots.sh` still failed with "the app surface ...
changed since the manual's screenshots were last taken", reproduced
identically both locally in this session and by GitHub's own CI runner on
that same commit (job logs for both the PR-triggered check on #194 itself
and the post-merge push-triggered `ci` run show the identical failure).

This was the guard blocking **all** CI on `universal-till` (the `build`
job fails early on this guard, before build/test/e2e even run), which in
turn was the proximate cause of universal-till#191/#192/#193 being unable
to confirm green — see ut-docs#367.

The two hash implementations (`scripts/ci/guard-docs-shots.sh` in Python,
`e2e/tests-docs/lib.js` in JS) are a faithful mirror of the same spec and
agree with each other on this canonical Linux checkout — running
`node e2e/tests-docs/write-manifest.js` here (no screenshot recapture,
just recomputing hashes from files already on disk) reproduces the exact
value the Python guard independently computes. Only the *previously
recorded* value in `manifest.json` was the outlier — most likely an
environment artifact (e.g. line-ending handling) in whatever machine
originally ran `make docs-shots` for #194, not a live algorithmic
divergence. The actual screenshots and topic hashes are unaffected and
still current — nothing has touched `web/help/img/`, `web/ui/`,
`web/public/`, or `internal/pages/` since #194 itself.

## Independent review

Fresh-context Sonnet subagent (`complexity:easy` tier — mechanical,
single-field correction). Verdict: **correct and safe to merge as-is**,
no blocking findings. Independently reproduced the failure (`git stash`
the fix, guard fails with the exact reported message) and the fix
(`git stash pop`, guard passes), independently recomputed the correct
hash via both a fresh Node one-liner and a standalone Python snippet
(both match the staged value), confirmed no screenshots are stale
(`git log` shows nothing has touched the affected paths since #194), and
re-ran the full sanity gate (`go build`, `go vet`,
`guard-data-access.sh`, `guard-i18n.sh` — all green, as expected for a
diff that touches no Go or i18n content). No secrets/client-identifying
content in the diff (it's a single hex hash).

## Verified beyond automated tests

- `bash scripts/ci/guard-docs-shots.sh` — red before, green after, on
  this exact commit tree.
- Confirmed via two independent recomputations (Node + standalone
  Python) that the corrected value is the one both canonical
  implementations agree on.
- `go build ./...`, `go vet ./...`, `guard-data-access.sh`,
  `guard-i18n.sh` — all green (no code touched; run as a sanity check
  per the standing gate rule).

## Safe to merge

Yes. Single-field, non-code diff; reproduced both the failure and the
fix live; independent review found nothing blocking.

## Deferred / follow-up

Filed as a new Backlog card (not fixed here, out of scope for this
mechanical correction):
- Root-cause *why* #194's generation environment recorded a wrong
  `surface_sha256` in the first place, so a future regen doesn't
  silently record a bad fingerprint again (leading theory: line-ending
  handling on whatever machine ran `make docs-shots`, unconfirmed).
- A cheap CI check that runs the Python and JS surface-hash
  implementations against a shared fixture tree and asserts equality, so
  a genuine future divergence between the two is caught mechanically
  instead of via a red `main`.
