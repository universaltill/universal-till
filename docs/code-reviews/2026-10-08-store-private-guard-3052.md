# Review — app stores stay private until launch: CI guard (ut-docs#3052)

**Date:** 2026-10-08 · **Card:** ut-docs#3052 (p1, complexity:medium) ·
**Branch:** `feat/3052-store-private-guard` · **Companion:** ut-docs
`reference/store-distribution.md` (branch `docs/3052-store-distribution`).

## What shipped

- `scripts/ci/guard-store-private.sh` — fails CI if any workflow, composite
  action, release script (`.sh .go .rb .py .js .mjs .ps1`), Gradle file,
  Makefile or fastlane file can reach a public store audience: App Store
  review submission (deliver/appstore/upload_to_app_store,
  submit_for_review, ASC review-submission APIs), external TestFlight, a
  Play track that is not literally `internal` (allow-list across key, JSON,
  flag, call and API-path forms, so variables and custom closed tracks fail),
  Play promotion, a Play upload that never names the internal track (per
  upload: its command line incl. `\` continuations, or the step of an
  action/fastlane call), and a Microsoft Store submission without a package
  flight. Quote-aware comment stripping; `.git`, `.claude`, `node_modules`,
  `vendor` pruned. Reviewed exception: same-line
  `store-release:allow ut-docs#<N>` naming the owner-approved card.
- `scripts/ci/guard-store-private_test.sh` — 54 fixture cases, each
  `expect_fail` asserts the specific rule tag, plus the real tree.
- `.github/workflows/ci.yml` `build` job runs both.

iOS was already TestFlight-internal only (`ios-testflight.yml`,
`ios-testflight-workflow_test.sh`, `asc-testflight-check`); no Play or
Microsoft Store upload workflow exists yet, so the guard fences them ahead
of #2481/#2480.

## Review (independent, Fable; two rounds)

Round 1 — not safe to merge; all fixed with tests:
- S1 (should-fix) play-default-track was file-scoped: a step *name* saying
  "track internal" or an earlier internal upload waived a later
  production-default upload. Now per upload.
- S2 deny-list of track values let custom closed tracks, JSON
  `"track":"production"` and SDK variables through. Now an allow-list.
- S3 the allow marker didn't work for `uses:`/multi-line upload forms.
- S4 composite actions, Makefile, Supplyfile/Deliverfile, `.ps1` unscanned.
- S5 MS Store: StoreBroker, Partner Center REST, any word "flight" waived.
- S6 Gradle Play Publisher `promote*Artifact` / `--promote-track` missed.
- N1 fastlane `appstore` alias; N2 `#` inside a string hid the command;
  N4 unpruned tree walk.

Round 2 (scoped to the fix) — safe once these landed; all fixed with tests:
- fastlane `supply(...)` alias not treated as an upload.
- `curl -f` matched the msstore `-f` flight exemption (now only with `msstore`).
- a CLI upload's window ran on to later prose/curl naming the internal
  track (CLI uploads now end at their logical line).
- bare `deliver` action.

Accepted (fail safe or documented): N3 — a future `track=$1` in a script
under `scripts/` is flagged (escape: the marker); `with:` before `uses:`
in a step is flagged; a Go identifier like `publishedBundle` would be
flagged. Known gaps listed in the script header: runtime-built commands or
API paths, releases outside the scanned files; store permissions (Play SA
limited to testing tracks) are the second fence.

## Verified

- Every new fixture case fails against the pre-fix guard and passes after
  (mutation run per round: 10, 17 and 5 failures respectively).
- Runs under mawk 1.3.4 (ubuntu-latest's awk); `shellcheck` 0 issues;
  guard passes on the real tree; `go build ./...` clean (no Go change).
- No UI surface touched (no UX/Tester visual pass needed).

**Verdict:** safe to merge.
