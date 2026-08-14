# Review: lang-pack-drift advisory pull_request check (ut-docs#669)

**Date**: 2026-08-14
**Card**: universaltill/ut-docs#669 — "lang-pack-drift only runs on push to main, so every core locale key reddens main and needs two follow-up PRs"
**Complexity**: medium
**Reviewer model**: fresh-context Opus subagent (per this card's `complexity:medium` tier — see `scrum-master` skill's model routing)

## Problem

`lang-pack-drift` (ut-docs#299) only ran on `push: branches: [main]`, so a
core PR adding a `web/locales/en.json` key merged clean and only turned
`main` red *after* merge — three separate core changes on 2026-08-13
alone (ut-docs#591, #634, #659) each needed a follow-up cleanup PR pair
in `ut-plugin-language-de`/`-es`.

## What shipped

- `.github/workflows/lang-pack-drift.yml`:
  - `pull_request` trigger added, scoped via `paths:` to
    `web/locales/en.json`, `scripts/ci/check-lang-pack-drift.sh`, and the
    workflow file itself — a PR that touches none of these never runs
    this workflow at all (no cost, no noise, and it's what let the guard
    trial its own fix on a PR rather than needing to land untested).
  - `push: branches: [main]` unchanged — still the sole blocking trigger.
  - The step now runs under explicit `shell: bash`, captures the drift
    script's real exit status (`|| status=$?`), and:
    - always writes a fenced `$GITHUB_STEP_SUMMARY` block with the
      script's output (works whether the run passes or fails);
    - on a `pull_request` run that found drift, emits a `::warning::`
      annotation naming `web/locales/en.json` and **force-exits 0** —
      this is the actual advisory mechanism (see "What the review found
      and fixed" below for why `continue-on-error` alone doesn't do
      this);
    - on every other trigger (`push`, `workflow_dispatch`), exits the
      script's real status — unchanged blocking behavior.
  - No `permissions:` change — stays `contents: read` only. The summary/
    annotation approach was chosen specifically so this job, which
    executes an unpinned script fetched from an external pack repo,
    never needs `pull-requests: write`.
- `CLAUDE.md`: new bullet in the i18n section documenting the check,
  including that it's `paths:`-scoped (absent from a PR's check list is
  normal) and an explicit warning never to add it to branch protection's
  required checks (see finding 6 below).

## Independent review (Opus, fresh context) — 1 CRITICAL, 1 HIGH (self-caught via the HIGH's own recommended re-trial), 2 MEDIUM, 3 LOW

**CRITICAL, fixed — the guard was a silent no-op on every trigger,
including the blocking one.** First diff version piped the script
straight into `tee` with no `shell: bash` set. Actions' default shell
(`bash -e {0}`) has no `pipefail`, so the pipeline's exit status was
`tee`'s — always 0 — regardless of what the drift script found. The
reviewer proved this from the live trial's own job log: the script's
final line was `one or more language packs have drifted from core`, yet
the step reported `success`, no `##[error]` line, nothing. Merging that
version would have made ut-docs#299's guard permanently green on `main`
regardless of real drift — strictly worse than the bug this card exists
to fix. Fixed: explicit `shell: bash` plus `|| status=$?` capture and
`exit "$status"`, independent of pipefail.

**HIGH, self-caught after the reviewer flagged it as unverified —
`continue-on-error` does not make a job's own check-run conclusion
`success`.** The reviewer's finding 2 noted the original trial couldn't
actually distinguish `continue-on-error` working from the pipe bug
masking everything, and called for a re-trial with the *step* showing
failure and the *job* showing success. Re-running live
(universal-till#337) with the CRITICAL fix in place showed something
worse than "unverified": the job's own check run reported `conclusion:
failure` outright — `continue-on-error` at job level only affects the
*workflow run's* top-level conclusion, not the individual job's check
run that the PR's Checks tab and branch protection actually read.
`mergeable_state` came back `unstable` (mergeable today only because this
check isn't a required status check yet) — a red X on every PR that
touches `en.json` regardless of whether that PR caused the drift, and a
real block waiting to happen the moment someone adds it as required.
Fixed by dropping reliance on `continue-on-error` entirely: the advisory
path now force-exits 0 in code after emitting the warning, so the check
is unambiguously green independent of any GitHub Actions/branch-
protection semantics. `continue-on-error` was removed from the job.

**MEDIUM, fixed — the advisory signal was invisible on the PR itself.**
`continue-on-error: true` alone leaves a plain green tick with no
in-context signal; AC #1 ("the author is told before merge") needs more
than a job you have to know to open. Fixed: `::warning::` workflow
command, which needs no extra permissions and surfaces on the PR's
Files-changed tab where the author is already looking, having just
touched `en.json`.

**MEDIUM-LOW, fixed — unfenced step summary would mangle future
output.** Raw script output teed into `$GITHUB_STEP_SUMMARY` renders as
one collapsed Markdown paragraph; any future `#`/`*`/backtick in pack
output would be interpreted or stripped rather than shown verbatim. AC
#2 ("names the exact missing keys") depends on this being legible on a
PR, not just present. Fixed: wrapped in a ` ``` ` fence, closed
unconditionally (the fence-close line runs regardless of the script's
exit status, via the `|| status=$?` capture rather than letting `-e`
abort mid-block).

**LOW, fixed — CLAUDE.md described behavior the first diff version
didn't actually have.** Corrected once findings above landed; the
bullet is accurate to the shipped mechanism now.

**LOW, fixed — no note on the `paths:` + required-checks foot-gun.**
Flagged by the reviewer as the single most likely way a future
maintainer breaks this: if `lang-pack-drift` is ever added to branch
protection's required checks, every PR that doesn't match `paths:` gets
no check run at all, which required-status-check gates read as
permanently pending. Added an explicit warning comment in the workflow
file plus a CLAUDE.md line.

**LOW, taken — the guard's own files weren't in `paths:`, so it couldn't
self-test on its own PR.** This is exactly why the CRITICAL bug needed a
separate throwaway trial PR to surface at all. Added
`scripts/ci/check-lang-pack-drift.sh` and the workflow file itself to
`paths:` (near-zero cost — these change rarely) so a future edit to the
guard can verify itself on its own PR directly.

**Confirmed correct by the reviewer, no changes needed:** no security
regression (`permissions: contents: read` unchanged, `persist-
credentials: false` retained, no new API surface — the summary/
annotation approach was specifically chosen over a PR comment to avoid
ever needing `pull-requests: write` on a job that runs an unpinned
external script); `pull_request` — not `pull_request_target` — is the
correct trigger (no elevated token/secrets on fork PRs, matching
`ci.yml`'s existing threat model); the `paths:` scoping genuinely
minimizes how often the unpinned pack-script fetch executes.

## Verified against live state (not synthetic) — three separate live GitHub Actions trials

1. **First trial (universal-till#336, closed unmerged):** a throwaway PR
   adding one junk key only to `en.json` (based on the then-current
   diff) confirmed the `pull_request` trigger fires and is correctly
   `paths:`-scoped — but also (via the reviewer's log analysis, not
   caught by this trial's own pass/fail alone) surfaced the CRITICAL
   pipe-swallows-exit-code bug.
2. **Second trial (universal-till#337, closed unmerged), first pass:**
   same throwaway-key technique against the CRITICAL fix. Proved the fix
   works (script's real failure now visible in logs) and, live, proved
   the HIGH finding: check conclusion was `failure`, not the `success`
   the design assumed from `continue-on-error`.
3. **Second trial, re-run after the HIGH fix (same PR #337, branch force-
   pushed):** `lang-pack-drift` check conclusion `success`
   (`check-runs` API, id `94662777752`), two annotations present
   including `web/locales/en.json | lang-pack-drift: a language pack is
   missing key(s) added here...`. Confirms the advisory path is
   genuinely green with the signal visible.
4. **`workflow_dispatch` run on the same commit** (not a `pull_request`
   event, so it exercises the same code path as `push`): check
   conclusion `failure` (check-run id `94662891352`) — confirms the
   blocking path is unchanged and still fails on real drift, on the
   exact commit that reports green on the PR path.

Both throwaway PRs (#336, #337) closed unmerged. Local throwaway branches
deleted; the two remote throwaway branches
(`test/669-trial-throwaway-key`, `test/669-trial-v2-throwaway-key`)
could not be deleted — `git push --delete` and the REST ref-delete both
returned 403 from this session's outbound proxy (write access to that
API path isn't permitted through it). Both are unmerged, contain only a
single harmless locale-key commit each, and their PRs are closed — noted
here and in the issue close-out rather than silently left unmentioned;
follow-up (manual deletion, or a proxy-path allowance) isn't worth a
separate card on its own but is called out for whoever next has push
access outside this proxy.

## Verification beyond the automated suite

- `python3 -c "yaml.safe_load(...)"` on the workflow file, every
  iteration.
- Local repro under `bash -eo pipefail -c '...'` (Actions' real `shell:
  bash` semantics) for both the `pull_request`/advisory path and the
  `push`/blocking path, against real pre-existing drift on `main`
  (`settings.data.archives_purge_*` keys — a genuine, currently-red
  `lang-pack-drift` run on `main` itself, unrelated to this card).
- `go build ./...`: clean (no Go files touched).
- `bash scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`: all pass, every
  iteration.
- No UI/visible surface touched — CI tooling + docs only, tester skill's
  screenshot/driven-run requirement doesn't apply.

## Safe-to-merge verdict

Yes. The independent review's most valuable contribution was refusing to
accept the first version's trial as sufficient evidence — its finding 2
explicitly said the mechanism was unverified and specified the exact
pass criteria for a real trial, which is what surfaced the HIGH before
merge instead of after. Both the CRITICAL and the HIGH would have shipped
a check that either always passes (CRITICAL) or always fails once made
required (HIGH) — opposite failure modes, both worse than the status quo
this card set out to improve. All findings fixed and re-verified live
against real GitHub Actions, not just re-read.
