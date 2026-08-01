# Code review: idle auto-lock / kiosk idle-reset silently disabled from second boot onward

**Date:** 2026-08-01
**Scope:** `internal/pages/init.go`, `internal/pages/init_test.go`
**Trigger:** ut-docs#177 — found by the independent review of ut-docs#157
(SaveState atomicity fix), confirmed present on `main` before that change
too.

## What shipped

`Init()`'s boot-time "persist resolved defaults" step called
`common.SaveState` with a hand-copied, partial `common.RuntimeState{}`
literal (Theme/Currency/Country/Region/TaxInclusive/TaxRatePct/
AllowNegativeInventory only). `SaveState` writes every field it's given
unconditionally, except it guards `UIScale` (skips if `<= 0`) and
`OSKMode` (skips if empty) — `IdleLockMinutes`/`KioskIdleResetSeconds`
have no such guard. The partial literal always left both at their Go
zero value, so every boot persisted `0` for both, and the next boot's
`LoadState` read that `0` back as a real, explicitly-configured value
(distinct from "unset") — silently disabling the unattended-till
auto-lock (a security control) and the self-order kiosk's idle-reset,
with no error, no log line, and no UI indication.

Fix: pass the `state` struct `LoadState` already resolved straight
through to `SaveState`, instead of re-listing a subset of its fields.
This removes the whole bug class (a field-list that can silently drift
out of sync with `RuntimeState`), not just the two fields named in the
ticket.

## Independent review (different-model subagent, Opus)

Briefed with the diff, the bug mechanics, `common/state.go` in full, and
told explicitly to run things and independently re-verify the TDD claim
rather than trust it. It did:

- Ran `go build`/`go vet`/both guard scripts/the affected test packages —
  all clean.
- **Reverted just the fix**, reran
  `TestInit_IdleAndKioskDefaultsSurviveTwoConsecutiveBoots`, and got the
  exact claimed failure (`IdleLockMinutes = 0, want default 10`;
  `KioskIdleResetSeconds = 0, want default 60`); restored the fix,
  confirmed pass again.
- Ran the new test `-count=20 -race`: clean.
- Checked for the two recurring bug classes this pipeline watches for
  (missing `os.MkdirAll`, cwd-relative path instead of `paths.Data`) —
  not applicable, no file I/O in this diff.
- Confirmed calling `Init()` twice in one process is safe (fresh
  `http.NewServeMux()` per call, `plugins.Init` idempotent against the
  same DB, background loops tick at 30s and never fire inside the test).

### Findings and disposition

1. **MAJOR — the fix doesn't heal a till whose settings already hold the
   bad `"0"` values from a prior buggy boot.** Real finding, verified
   independently before dismissing it (not just asserted away): the
   buggy commit (`fcae24d`, 2026-07-31) is one day old, this repo has
   **zero git tags** (`git tag` → empty), and `.github/workflows/
   release.yml` only triggers on `push: tags: ["v*"]` — so no release
   has ever been cut, and no till (real or demo) has received this bug
   through the actual release channel. Combined with the product having
   no real shop of its own yet, there is no live install to heal. Not
   fixing now — a one-shot heal migration for a bug that's never shipped
   would be speculative complexity with no real target. If this
   assumption ever turns out wrong (an out-of-band/dev build reached a
   real till), it's a one-line, targeted fix at that point, not
   something to build defensively today.
2. **MINOR — new implicit ordering dependency.** `SaveState` now runs on
   the full `state`, which includes `UIScale`. Behavior is unchanged
   today only because this call happens *before* the `UT_UI_SCALE`
   env-provisioning resolution further down `Init()` — `state.UIScale`
   is still `0` at this point, so `SaveState`'s own `> 0` guard skips
   writing it, preserving the "never persist the env-derived
   provisioning scale" invariant. Fixed: added a comment on the
   `SaveState` call pinning this ordering explicitly, so moving the call
   later doesn't silently start persisting `UT_UI_SCALE` per-till.
3. **NIT — `OSKMode` now gets written on first boot** (`LoadState`
   always resolves it to a non-empty default, `"auto"`), where the old
   partial literal never included it. Idempotent, harmless, and
   arguably correct given the surrounding comment's own intent ("ensure
   defaults are persisted") — accepted as-is, not changed.
4. **NIT — strengthen the regression test.** The original version only
   asserted `LoadState`'s resolved view, which would also pass if
   `SaveState` were never called at all (absent key falls back to the
   same default). Fixed: added direct raw-store assertions
   (`store.Get(ctx, common.KeyIdleLock)` /
   `common.KeyKioskIdleReset` must literally read back `"10"`/`"60"`)
   alongside the resolved-view check, so the test proves the *persist*
   step specifically, not just that defaults resolve correctly from an
   empty store.
5. **Also found, out of scope for this card**: `go test ./... -shuffle=on`
   intermittently fails `TestCollectProblems_IncludesFailedPluginInstalls`
   — confirmed pre-existing (reproduces identically with or without this
   diff's new test present), caused by `collectProblems`'s
   `maxProblems = 20` cap racing against the process-global
   `logging.Recent()` ring under shuffle ordering
   (`cloudsync_wire.go:201-228`). Filed as a new Backlog card
   (ut-docs#219) rather than fixed here — unrelated to idle-lock/kiosk
   defaults.

## Verification

- `go build ./...`, `go vet ./...`: clean.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`:
  both green (diff touches no SQL, no user-facing strings).
- `go test ./internal/pages/... ./internal/pages/common/...`: clean.
- `go test ./...`: clean except the standing, pre-existing
  `internal/issuereport` root-in-container flake
  (`TestSaveCleansUpDirectoryOnWriteFailure`), unrelated to this change.
- `TestInit_IdleAndKioskDefaultsSurviveTwoConsecutiveBoots -count=20 -race`:
  clean (reviewer) and re-confirmed `-count=5 -race` after the two
  review-driven refinements (self).
- **TDD claim independently re-verified by the reviewer**: reverted the
  fix, confirmed the test fails with the exact claimed error, restored
  the fix, confirmed it passes.
- Real running binary, not just the test suite: built the actual
  `unitill-desktop`-equivalent server binary, booted it twice in a row
  against the same on-disk data directory (`UT_DATA_DIR`), and read the
  SQLite `settings` table directly after each boot —
  `auth.idle_lock_minutes=10`, `kiosk.idle_reset_seconds=60` after both
  boot 1 and boot 2 (the exact scenario the bug broke).
- Checked for no real client/shop name in test/demo data, and no
  secret-shaped literal — none present (this diff touches no seed/demo
  data or credentials at all).

## Verdict

**Safe to merge.** The independent review found one real, well-evidenced
gap (pre-existing corrupted installs) that was investigated and
confirmed not applicable today rather than silently dismissed, plus two
small real refinements (ordering comment, stronger test assertions) that
were applied. No blockers remain.
