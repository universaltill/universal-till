# Review: escalating device-wide PIN lockout (ut-docs#5141)

Branch `fix/5141-escalating-pin-lockout`. Built by Sonnet (Dev subagent);
reviewed by Opus 5.5 (different model), which also wrote the review fix.

## What shipped

- `internal/auth/service.go`: the device-wide lockout used to be a flat
  30 s after 5 failures, about 600 guesses an hour (a 4-digit PIN in
  ~17 h). Now each lockout in a row doubles (30 s, 1, 2, 4, 8 min) up to a
  15 min cap, about 20 guesses an hour. A correct PIN resets the failure
  count but not the level. The level decays only after an hour with no
  failure (`lockoutQuietReset` ≥ the cap). There is an injectable clock for
  tests.
- `auth.error.locked` loses its "wait 30 seconds" in en/ar/fa/tr. Each
  locale now uses its own existing `elevation.error_locked_out` wording.
  The key is unchanged and no placeholder changed.
- `ut-docs` `architecture/pos-auth.md` is updated (separate ut-docs PR).

## Scope note

The card's premise was a LAN attacker. ADR-0157's `languard` (2026-10-13)
already refuses non-local callers on `/login`, `/api/auth/*` and
`/api/cloud/*`. What still reaches the keypad endpoints is processes on the
till itself and networks the operator allows with `UT_UI_ALLOW_CIDRS`. The
card's other two mitigations (manager-only session on step-up endpoints, a
same-origin check on their POSTs) are defence in depth. They moved to
ut-docs#5156, which also tracks refreshing the packs' `auth.error.locked`
text.

## Findings

1. **High, fixed: concurrent guesses bypassed the limiter.** This predates
   the change. `checkPIN` read `locked()`, ran the slow PBKDF2 scan, and only
   then recorded the failure. Any number of parallel requests passed the
   check before the first one was counted, so neither the old nor the new
   rate bound held against a parallel attacker. The new
   `TestLockoutHoldsUnderConcurrentGuesses` reproduced it: 40 of 40
   concurrent guesses were checked. Fix: `beginAttempt` reserves an
   in-flight slot under the mutex (`failures + inFlight` must stay below 5)
   and `endAttempt` records the outcome. `checkPIN` and `ChangeOwnPINVia`
   both use the pair. A guess that hits a DB error counts as a failure
   (fail closed). A first draft counted every guess up front instead. That
   made a correct 5th PIN trip a lock and bump the level, which
   `TestLockoutLevelSurvivesSuccess` caught, so it became the slot count.
2. **Low, accepted: lockout availability.** Someone who can reach the
   keypad can keep it locked for up to 15 min at a time instead of 30 s.
   Signed-in sessions are unaffected, and this caller was already able to
   lock it out continuously. The trade is accepted for a ~30× slower
   brute force.

## Verified

- TDD: the Dev subagent's tests failed before its fix (`svc.now undefined`).
  The concurrency test failed with "40 concurrent guesses were checked,
  want at most 5" before the review fix and passed after it.
- `go test ./internal/auth/ -race`: ok. CI's deadcode guard then caught `Service.locked` left test-only after the slot rework; it moved into the test file. `gofmt`, `go build ./...` and
  `golangci-lint run ./...` (0 issues) are clean. The guards in `ci.yml`'s
  build job pass locally, except two that can't run in this container:
  `guard-deadcode-baseline.sh` (deadcode tool pinned to an older Go) and
  `guard-shellcheck-version.sh` (no shellcheck). CI runs both. The diff
  adds no shell.
- No UI surface changed beyond a message value. The help topics don't
  mention the lockout duration (grepped).

## Verdict

Safe to merge.
