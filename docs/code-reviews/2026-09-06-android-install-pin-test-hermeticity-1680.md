# Review: `androidInstallCheckNow` override in the cashier-PIN-gate test (ut-docs#1680)

**Branch:** `fix/1680-android-install-pin-test-hermeticity`
**Commit:** `6195e82`
**Complexity:** easy — Dev at Sonnet (inline), Review at fresh-context Sonnet subagent.

## What shipped

`TestAndroidInstallFormReachableByCashierButPINGated`
(`internal/pages/android_update_placement_test.go`) never overrode the
package-level `androidInstallCheckNow` var, so its wrong-PIN assertions
depended on whatever the real `updates.CheckNow` (a live network call)
answered at the moment the test ran. `POST /api/update/android-install`
checks freshness *before* the manager-PIN check by design — no update
available short-circuits to 200 `already_current` for *any* PIN, right or
wrong. Whenever the live check happened to report "already current", the
wrong-PIN assertion failed — not because the PIN gate was broken, but
because the test's own precondition (an update being available) was never
pinned down.

Found flaking `universal-till` PR #844's CI (`ut-docs#1650`)'s `build`
check, twice (including the one allowed re-run), unrelated to that PR's
own diff.

**Fix:** pin `androidInstallCheckNow` to `Available: true` for the
duration of the test, restored via `t.Cleanup` — the same pattern this
test's own siblings (`TestAndroidInstallRefusesWhenAlreadyCurrent`,
`android_update_session_auth_test.go`) already use.

## Independent review

Fresh-context Sonnet subagent (different instance, no exposure to the
implementer's reasoning) — **safe to merge, no blockers, no findings.**

Verified independently, not just re-stated from the commit message:
- Read the handler (`internal/pages/update_api.go:349-420`) and
  re-derived the exact race from its own control flow, matching the
  root-cause narrative.
- **Confirmed no collateral damage** — the one real risk on this diff,
  since an earlier draft of this fix used a broad `sed` that briefly
  clobbered `TestAndroidInstallRefusesWhenAlreadyCurrent`'s own
  `Available: false` before that mistake was caught and reverted pre-review.
  The reviewer verified the *committed* diff is exactly one hunk, 10
  insertions, 0 deletions, touching only the intended test, and that the
  sibling test still reads `Available: false` unchanged.
- **Reproduced the root cause itself**, not just trusted it: temporarily
  flipped the new override to `Available: false` in a scratch edit,
  re-ran the test, got the identical failure signature described in
  ut-docs#1680 (`a wrong PIN authorised an install:
  {"already_current":true,...}`), then restored and confirmed clean via
  `git status`/`git diff`.
- Ran `go build ./...`, `go vet ./...`, `gofmt -l` (clean), the
  `TestAndroidInstall*` group 3x (all green, including the target test),
  and the full `internal/pages/...` package once (green).
- Checked the two recurring bug classes (missing `os.MkdirAll`, a
  cwd-relative path where `paths.Data(...)` belongs) — not applicable,
  no file I/O in this diff.
- `CLAUDE.md` non-negotiables: no SQL outside `internal/data`/`internal/db`
  (test-only change), no user-facing strings/i18n touched, no real
  client/shop name as test data.

## Verification (implementer + reviewer, independently)

- `gofmt -l internal/pages/android_update_placement_test.go` — clean
- `go build ./...`, `go vet ./...` — clean
- `go test ./internal/pages/... -run TestAndroidInstall -v -count=3` —
  green every iteration
- `go test ./...` (full suite, all ~50 packages) — green
- `golangci-lint run ./...` — 0 issues
- Every CI-blocking guard under `scripts/ci/` (per `CLAUDE.md`'s current
  list) — all pass
- **TDD/root-cause reproduction** (both implementer and reviewer,
  independently): forcing `Available: false` reproduces the exact CI
  failure signature; restoring `Available: true` passes cleanly. Also ran
  the target test + full `internal/pages` package under `LANG=en_GB.UTF-8`
  and `LANG=de_DE.UTF-8`, matching every CI invocation of this package —
  all green.

## Deferred / out of scope

None. This is a self-contained, one-file test-hermeticity fix; no
user-facing behavior changed, so the manual/UX-guideline checks don't
apply.
