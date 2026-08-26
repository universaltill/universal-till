# Desktop-kiosk-overlay fresh-install bugs (ut-docs#1094)

**Date:** 2026-08-26
**Card:** ut-docs#1094 (follow-up from ut-docs#1040, a real first fresh-hardware
install on the product owner's Pi 5)
**Complexity:** hard
**Author (dev):** scrum-master pipeline cycle, inline (Sonnet)
**Reviewer:** independent Sonnet-orchestrated Opus subagent, isolated worktree

## What shipped

Three failures were reported from the first real fresh install of the
ut-docs#1040 desktop-kiosk-overlay feature. Two are fixed here, root-caused
and regression-tested; the third is documented, not claimed fixed.

1. **`unitill-pos provision-desktop-kiosk-defaults` failed inside the dpkg
   transaction, worked when run by hand afterwards.** Root cause:
   `internal/db`'s `migrate()` reads its current schema version once,
   unprotected, before applying each pending migration. On a genuinely
   fresh install, `postinstall.sh` restarts `unitill-pos.service` then
   immediately invokes this subcommand as a second connection to the same
   brand-new database — a second connection that reads a stale version
   before the service's own first-ever migration commits re-attempts a
   migration the service just applied: a genuine SQL error (a duplicate
   column or a `schema_migrations` primary-key conflict, not necessarily
   "table already exists" — see the review correction below), not a
   lock/busy-timeout one `busy_timeout` can wait out.
   **Fix:** `internal/app/provision.go`'s `openWithRetry`/`retryOnError`
   retries the whole `db.Open` (which re-reads the version fresh) with
   bounded backoff (8 attempts x 750ms), converging once the racing
   migration has landed.

2. **`unitill-desktop --install-autostart` reported success (exit 0, no
   warning) but wrote nothing.** `packaging/scripts/postinstall.sh` now
   clears an inherited `$XDG_CONFIG_HOME` before invoking it (Go's
   `os.UserConfigDir()` checks it before `$HOME`), and — the genuinely
   valuable half of this fix — verifies the autostart entry actually
   exists on disk before trusting the exit code, printing a loud,
   path-naming warning and recording `autostart_staged=false` honestly if
   not.

3. **`display.window_mode` still read "normal" on a live `control=live`
   poll even after a manual re-provision + service restart.** Traced the
   read path (`pages.Init` → `LoadState` → `NewShellChannel` →
   `MarkExitPath`) and it looks structurally correct for a genuine
   restart. **Not fixed here** — see "What the independent review found"
   and "What's still open" below.

## What the independent review found

The first draft of this change had **three blockers**, all confirmed real
and fixed:

1. **The `$HOME` diagnosis for failure 2 was factually wrong.**
   `runuser -u USER --` *does* reset `$HOME`/`$SHELL`/`$USER`/`$LOGNAME` to
   the target user's own by default — util-linux's `-m`/`--preserve-
   environment` is the opt-*out*, not the opt-in the first draft assumed.
   The reviewer measured this directly (`runuser -u uttest -- printenv
   HOME` → the real target home, even with the caller's own `$HOME`
   unset). The `env HOME=...` override in the first draft was therefore a
   no-op for the reported symptom. **Fix:** dropped the `HOME` override
   entirely; kept and generalized the real remaining gap, `$XDG_CONFIG_HOME`
   (which `runuser` does *not* reset, and which `os.UserConfigDir()` checks
   *before* falling back to `$HOME`).

   The reviewer also surfaced a much better-fitting alternative hypothesis
   for the *actual* reported symptom (exit 0, wrote nothing): a locally
   built or non-release `.deb` could carry `cmd/unitill-desktop`'s
   `//go:build !desktop` stub `main()` (`stub.go`), which ignores
   `--install-autostart` entirely and just prints two lines and exits 0 —
   matching the report exactly. `.github/workflows/release.yml` does build
   the shipped artifact with `-tags desktop`, so an official release
   should carry the real binary; **confirming which binary was actually on
   the reporter's Pi is the next step**, not attempted here (needs the real
   hardware: `strings /opt/unitill/bin/unitill-desktop | grep 'native
   desktop shell'`). Documented on the issue rather than guessed at.

2. **`TestDesktopOverlayBranchAutostartVerifiesTheFileNotJustTheExitCode`
   didn't test its own claim.** It compared line *indices*, which can't
   express nesting — the reviewer mutated the script to move
   `AUTOSTART_STAGED=true` unconditionally outside the file-existence
   check (the exact regression the test's own name promises to catch) and
   it still passed. **Fix:** rewrote as
   `TestDesktopOverlayBranchAutostartStagesOnlyWhenTheFileActuallyExists`,
   which actually *runs* the extracted staging snippet (same convention
   this file's own header already sets for the display-manager
   predicates) against a fake `runuser` that independently controls exit
   code and whether the target file gets written. Re-ran the reviewer's
   own mutation against the fix: now fails correctly.

3. **`TestDesktopOverlayBranchAutostartResolvesLoginUsersHome` was
   tautological.** It asserted only that *some* line resolves a home
   directory and *some* line passes `env HOME=`, without checking whose
   home. The reviewer changed the script to resolve *root's* home instead
   of the overlay user's and the test still passed. **Fix:** replaced with
   `TestDesktopOverlayBranchAutostartHomeAndConfigHomeAreExplicit`, which
   checks for the real fix (`env -u XDG_CONFIG_HOME`) and explicitly fails
   if the disproven `env HOME=` override reappears. Verified by
   mutation (re-adding the override) that it now fails correctly.

Five **should-fix** findings, all addressed:

4. **`$XDG_CONFIG_HOME` was the one real env vector the first draft left
   open** — closed by clearing it (`env -u XDG_CONFIG_HOME`) in the
   `--install-autostart` invocation.
5. **An empty `getent` home field could break a case that worked before.**
   The (now-removed) `HOME` override would have forced `HOME=""` in that
   edge case; with it gone, `runuser` handles this correctly on its own.
   The verification step itself also explicitly skips the file-existence
   check (falls back to trusting the exit code) when `$OVERLAY_HOME` is
   empty, rather than checking a meaningless path.
6. **The read-back verification in `provisionDesktopKioskDefaults` was
   dead code at the wrong layer, and its error path made failure 3
   *worse* when it (hypothetically) fired.** `SetMany` and a same-process
   `Get` share one `*sql.DB` after a commit, so SQLite's own consistency
   guarantees make a same-function read-back unable to ever catch
   anything real — the actual failure-3 mechanism (see below) happens
   later, in a different process state. Worse, erroring out of this
   function on a spurious mismatch would skip `systemctl try-restart`,
   the audit row, and the marker — exactly wrong. **Removed entirely**,
   with a comment explaining why and pointing at the real mechanism.
7. **`db.Open`'s error paths leaked the `*sql.DB` pool**, amplified 8x by
   the new retry (each failed attempt would leak its own pool of
   connections against the very database the retry is waiting on).
   **Fixed in `internal/db/db.go`**: every error path now closes `sqlDB`
   before returning.
8. **The migration race is symmetric — the *service* can lose it too**,
   which would fail `unitill-pos.service`'s own startup (a dead till on a
   fresh install, worse than this subcommand failing). Not fixed here
   (the real fix, locking `migrate()`'s version-read-and-apply inside one
   transaction, touches every `db.Open` call in the app and needs its own
   careful review) — documented as a residual risk in `provision.go`'s
   comment and on the issue.

Two **nits**, both addressed: the two sleep-boundary tests
(`TestRetryOnError_FirstTryAloneNeverSleeps` /
`_NeverSleepsAfterTheFinalAttempt`) used a real `time.Hour` delay and
asserted on elapsed wall-clock time *after* the call returned — a
regression that slept unconditionally would hang the whole test run for
that hour rather than failing fast. Rewrote to run `retryOnError` on its
own goroutine with a 2s timeout via `select`. And this review record
itself was the other nit (missing on the first draft).

## What was verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./...` — clean.
- `go test ./...` (full suite) — clean; `internal/app`, `internal/db`,
  `packaging` specifically re-run with `-v`.
- CI guards re-run: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh` —
  all pass (no i18n/UI surface touched beyond the ones already covered).
- **Mutation-tested the two rewritten packaging tests myself**, re-running
  the reviewer's own two mutations (unconditional `AUTOSTART_STAGED=true`;
  a reintroduced `env HOME=` override) against the fixed test code —
  both now fail correctly, where the first draft's tests did not.
- `bash -n packaging/scripts/postinstall.sh` — syntax clean (no
  `shellcheck` available in this sandbox).

## What's still open (not claimed fixed)

- **Failure 3's exact mechanism** (`window_mode` reverting to "normal").
  The leading hypothesis, from tracing `internal/pages/common.SaveState`
  (`state.go:306-328`): it rewrites the *whole* settings map from
  whatever `RuntimeState` a later wizard/Settings save was built from,
  unconditionally including `window_mode` — a stale or zero-valued
  snapshot at that later point would silently revert the seeded value,
  well after this provisioning run has already exited successfully. This
  sandbox has neither `gtk+-3.0`/`webkit2gtk-4.1` (so `cmd/unitill-desktop`
  can't even build — the same gap ut-docs#1071 found for per-PR CI) nor
  real Pi hardware, so the live `control=live` poll path can't be driven
  end-to-end here. Left open on the issue rather than shipping an
  unverifiable fix.
- **Which binary was actually on the reporter's Pi** (real implementation
  vs. the `!desktop` stub) — the leading hypothesis for failure 2's exact
  symptom, needs real-hardware confirmation.
- **The migration race's symmetric risk to the service itself**
  (should-fix 8 above) — documented, not fixed.

## Scope note

Touches `internal/app/provision.go` (+tests), `internal/db/db.go`,
`packaging/scripts/postinstall.sh`, and `packaging/desktop_overlay_test.go`.
No schema change, no new SQL outside the repository layer, no new
user-facing string. `internal/db/db.go`'s leak fix is a small, narrowly
scoped correctness fix (close-on-error) with no behavior change on the
success path, applying to every `db.Open` caller in the app, not only this
one — reviewed as part of this change since the new retry is what
amplified its impact from 1x to 8x.
