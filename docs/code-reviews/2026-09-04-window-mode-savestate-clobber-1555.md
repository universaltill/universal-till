# Code review: SaveState clobbering out-of-band window_mode/launch_on_startup (ut-docs#1555)

**Branch:** `fix/1555-window-mode-savestate-clobber`
**Reviewed by:** independent Opus subagent (worktree-isolated).

## What shipped

Part 1 of a two-part product-owner report on the Pi satellite (Part 2,
OS panel/keyboard suppression, needs real hardware — split to
ut-docs#1564). Root cause: `provision-desktop-kiosk-defaults` (run as a
**separate process** by `packaging/scripts/postinstall.sh`) writes
`display.window_mode=kiosk` / `display.launch_on_startup=true` straight
to the DB. The main server process's in-memory `common.Deps.State` never
observes this. Any later `common.SaveState` call built from a
`d.CurrentState()` snapshot that predates that write — the first-boot
wizard being the most common one — persisted the WHOLE `RuntimeState`
unconditionally, silently reverting the out-of-band values back to their
stale defaults.

Fix: `RuntimeState` gains `WindowModeChanged`/`LaunchOnStartupChanged`
(explicit per-save intent, zero-value false). `SaveState` re-reads the
live persisted value and lets it win unless the caller marks intent; the
two handlers that deliberately change these fields
(`POST /api/settings/window-mode`, `.../launch-on-startup`) set the flag.

## Independent review — round 1 verdict: NOT SAFE TO MERGE AS-IS

Found one real, demonstrated blocker (F1) plus five lower-severity/nit
findings. Full findings, ranked:

1. **F1 (HIGH, blocker) — sticky sentinel defeats the guard permanently
   after the first use.** The two handlers cache `st` (with `*Changed =
   true`) via `d.SetState(st)`. `Deps.State` is long-lived, so from that
   point on **every** subsequent `SaveState` call in the process —
   unrelated theme/currency/idle-lock saves, the wizard, anything —
   inherited `WindowModeChanged: true` from `d.CurrentState()` and skipped
   the re-read entirely, fully restoring the original clobber. Reviewer
   reproduced with a throwaway test (removed after use, replaced with the
   permanent tests below).
   **Fixed:** `Deps.SetState`/`Deps.UpdateState` now unconditionally clear
   both flags before caching — they are per-save intent, never a property
   of cached state. 3 new regression tests
   (`TestDeps_SetStateClearsWindowAndLaunchChangedFlags`,
   `TestDeps_UpdateStateClearsWindowAndLaunchChangedFlags`,
   `TestSaveState_DeliberateChangeDoesNotStickyBlockLaterOutOfBandProtection`
   — the last one reproduces the reviewer's exact 3-step sequence end to
   end through the real handler shape). TDD-verified personally: reverted
   just the two-line fix, all 3 tests failed with the exact predicted
   symptom values, restored, all pass again (see below).
2. **F2 (MEDIUM) — doc comment overclaimed "removes the race entirely."**
   `SaveState` takes `st` by value; its own corrected value never reaches
   back to the caller's `d.SetState(st)`, so the IN-MEMORY half (`GET
   /api/window-mode`, autostart reconciliation) still needs
   `postinstall.sh`'s `systemctl try-restart`. A maintainer reading the
   original comment could reasonably conclude that restart is now
   redundant and remove it, reopening the "shell deletes its own
   autostart entry" failure that script's own comment documents.
   **Fixed:** comment now states plainly this closes the persistence race
   only, and the restart is still required for the in-memory half.
3. **F3 (LOW) — re-read failed open on a transient `store.Get` error.**
   An error mid-read fell through to trusting the caller's possibly-stale
   value, silently reproducing the bug for that one save.
   **Fixed:** on error, the affected key is omitted from the write
   entirely (persisted row left untouched) instead of writing a
   potentially-stale value.
4. **F4 (LOW) — read-then-write isn't inside `SetMany`'s transaction.**
   Narrow (operator-paced, single-operator writes) — documented with a
   comment rather than wrapped in an explicit transaction for two
   single-key reads.
5. **F5 (NIT) — `TestSaveState_RoundTripsThroughLoadState` has a hidden
   dependency on an empty store** (the round trip for
   WindowMode/LaunchOnStartup only holds because there's no existing row
   to re-read). Documented with a comment pointing at the tests that do
   cover a seeded store.
6. **F6 (NIT)** — corrected characterization: only 2 of the original 3
   tests are bug reproductions; `TestSaveState_HonorsExplicitWindowModeChange`
   is a legitimate non-regression test for the opt-in path.

## Round 2 (personal re-verification, not a second AI review pass)

F1 is a genuine correctness bug but not itself money/tax/data-loss/
security — per this pipeline's process-depth rule, a second full review
round is earned only by a blocker in that class, so I fixed F1/F2/F3/F5
personally and re-verified rather than spawning a second Opus pass,
consistent with "one review round unless the first finds a blocker-class
issue."

- Applied all four fixes above.
- Added the 3 new tests, TDD-verified F1's fix specifically (revert just
  the two-line `SetState`/`UpdateState` clearing → all 3 new tests fail
  with the exact predicted stale value → restore → all pass).
- Full gate re-run on the fixed diff: `gofmt -l .` clean, `go build ./...`
  clean, `go vet ./...` clean, `go test ./...` (every package) green, no
  `-race` issues in the affected packages.
- `guard-docs-shots.sh`: `internal/pages/common/{deps,state}.go` carry
  zero route registrations of their own, but per the guard's own documented
  rule ("a file with zero registrations is KEPT — it can still feed a
  screenshotted page's template data") they're part of the hashed surface,
  since `RuntimeState` feeds Settings/Display rendering. Re-ran
  `make docs-shots` (96/96 pass) after these edits; guard green.
- `guard-i18n.sh`, `guard-help-topics.sh`, `guard-data-access.sh`: all
  green — this diff adds no new user-facing strings or routes.

## What was verified beyond automated tests

Two full TDD revert-restore cycles (the original fix, then F1's fix)
with real induced failures showing the exact stale-value symptom, not
just "tests pass." No hardware verification needed for Part 1 — it's a
Go-level persistence bug, reproducible entirely with a real SQLite-backed
test store.

## Deferred / out of scope

- F4 (transactional read-then-write) — documented, not wrapped; narrow
  and accepted.
- Part 2 of the original report (Pi OS panel/keyboard suppression) —
  split to ut-docs#1564, needs real-hardware verification a cold cloud
  session cannot do.

## Safe-to-merge verdict

**Yes**, with F1/F2/F3/F5 fixed. `merge_method: "merge"` per this repo's
standing convention (ut-docs#250).
