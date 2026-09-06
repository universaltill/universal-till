# Code review: ai.identify overlay camera-error e2e coverage (ut-docs#1559)

**Date:** 2026-09-06
**Branch:** `fix/1559-ai-identify-camera-error-coverage`
**Card:** universaltill/ut-docs#1559
**Complexity:** easy

## What shipped

`ut-docs#1559` (found during independent review of universal-till#772 /
ut-docs#1292): the `scan.camera` (barcode-scan) overlay's `err.name`
branching got e2e coverage in `camera-error-branching-1292.spec.ts`, but
the `ai.identify` overlay duplicates the identical branching logic in its
own IIFE (`web/public/app.js`) with zero coverage of its own — a future
edit renaming one overlay's `data-msg-*` attribute and not the other's
would ship a silent blank/wrong error message, invisible to both
`guard-i18n.sh` (the key still exists, just under the wrong attribute)
and the existing test (different overlay).

Unlike the barcode-scan overlay, `ai.identify`'s markup doesn't exist in
the DOM at all unless the server resolves `.aiIdentify` true
(`{{ if .aiIdentify }}` in `web/ui/pages/index.html`), so it can't join
the shared default-project till. Added a third Playwright project/server
(mirroring the existing default/auth split): `run-till-ai.sh` sets
`UT_AI_ENDPOINT` to a non-routable address (safe — `err.name` branching
happens inside `getUserMedia`'s `.catch()`, before any real identify API
call), and a new spec `camera-error-branching-ai-identify-1559.spec.ts`
mirrors the existing 1292 spec's 4 cases against
`#ai-identify-open`/`#ai-identify-status`.

## Independent review

Spawned a fresh-context Sonnet subagent (easy-complexity routing). It
checked out the PR in an isolated worktree and verified everything
itself rather than trusting the description:

- Confirmed the true diff (3 files, +166/-5) via the actual merge-base —
  the branch's raw `git diff --stat` against a moving `main` initially
  looked like 8 files/2 unrelated commits' worth of noise (this branch
  was cut before #827/#828 merged); flagged as a hygiene note, not a
  defect, since GitHub tests the merge ref (already green) not the raw
  diff. Fixed by merging current `main` into the branch before finalizing
  this review record.
- Ran the new spec in isolation (4/4 pass) and the full CI-equivalent
  `CI=1 npx playwright test` with no project filter — the exact command
  `.github/workflows/e2e.yml` runs — getting **352 passed** (329 default +
  19 auth + 4 new), matching the real GitHub Actions run on the PR
  (`playwright`/`build`/`authors`/`contract`/`e2e` all green).
- Traced the actual mechanism rather than taking it on faith:
  `internal/pages/index_page.go:149` → `aiService(...).Enabled()` →
  `internal/ai/ai.go`'s `FromEnv()`/`New()` confirm a bare
  `UT_AI_ENDPOINT` really does flip `.aiIdentify` true, and
  `web/ui/pages/index.html:525-547` confirms the whole overlay block is
  template-gated behind it.
- **Re-did the TDD verification itself**, not on trust: renamed
  `data-msg-camera-busy` on the ai-identify overlay only, re-ran the
  `NotReadableError` test → real assertion-mismatch failure (not a
  timeout/crash), reverted, 4/4 pass again.
- Programmatically matched all 88 e2e spec files against the config's
  regexes: 84 default + 3 auth + 1 ai-identify = 88, zero overlap, zero
  gap — ruled out a spec silently running twice or never.
- Confirmed the dummy `UT_AI_ENDPOINT` is genuinely never reached: the
  real identify network call lives in a separate `identify()` function
  wired to `#ai-identify-capture`, which none of the 4 new tests trigger
  (they stub `getUserMedia` to reject synchronously, so no stream is ever
  established).
- Checked `run-till-ai.sh` against the two recurring bug classes this
  pipeline watches for (missing `os.MkdirAll`, cwd-relative path where
  `paths.Data(...)` belongs) — N/A, it follows the same
  mktemp+trap+build-from-fresh-dir pattern as sibling `run-till.sh`.
- Confirmed author (`Farshid Mirza <...>`, Claude only in the
  `Co-Authored-By:` trailer) and scanned for secrets/real names — clean.
- Confirmed no manual/help-topic update is owed: the diff touches nothing
  under `web/ui`, `web/locales`, `web/help`, or `internal/pages`.

**Verdict: SAFE TO MERGE.** No blocker-class finding (no
plugin-signature-bypass-class or false-pass-test-class issue). One
informational hygiene note (branch behind `main` at review time) —
resolved by merging `main` in before finalizing.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...` — clean (no Go changes; config/test-only).
- Every CI-blocking guard in the `build` job — all pass (only
  `guard-e2e-fixtures-import.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh` are logically reachable by this diff; ran
  the full set anyway).
- New spec run personally in isolation (pre- and post-merge-with-main),
  with the TDD break/restore, and as part of the full 3-project suite —
  then independently re-run by the reviewer in its own worktree with the
  same TDD re-verification and full-suite check.
- Real GitHub Actions CI on the PR: `build`, `authors`, `contract`, `e2e`,
  `playwright` all green.
- No real client/shop name or secret-shaped literal in the diff.

## Deferred / out of scope

- None — this card's scope (coverage-only, non-goal: not re-litigating
  #1292's branching logic itself) is fully closed by this diff.

## Safe-to-merge

Yes. Merged via `merge_method: "merge"` (never squash/rebase — see the
`reviewer` skill's "Merge method" note, ut-docs#250) once CI is green.
