# Code review: pairing "joined" screen restart (ut-docs#1550)

- **Date**: 2026-09-06
- **Card**: universaltill/ut-docs#1550
- **Repo/branch**: `universal-till`, `fix/1550-pairing-joined-restart`
- **Complexity**: hard — Dev at Fable, review at Opus (independent subagent)

## What shipped

`web/ui/partials/pairing_wait.html`'s "joined" branch used to render only
"✓ Joined: {shop} — restart this till to finish" with no button — a real
dead end reported by a pilot user with no shell access on a Pi kiosk.
Root cause: `completeJoin` (`internal/pages/sync_api.go`) stages a
downloaded DB snapshot via `db.StageRestoreFromReader`, and
`db.ApplyPendingRestore` only ever runs once, before `db.Open`, at process
startup (`internal/app/app.go`) — the till genuinely has to restart for a
join to take effect.

- **New `internal/procrestart` package**: schedules an in-place
  `syscall.Exec` restart on non-Windows (same PID, same mechanism
  `internal/selfupdate` already uses in production, independently
  implemented rather than reused — `selfupdate`'s seams are unexported and
  it's a production-critical path with no reason to be touched here).
  Windows has no in-place exec (`Supported()` false there); the UI falls
  back to an honest "close and reopen the app" instruction instead of a
  button that would do nothing (native Windows restart tracked separately,
  ut-docs#1614).
- **Two new endpoints**: `POST /api/sync/pairing-restart` (manager-gated)
  and `POST /api/setup/pairing-restart` (first-boot, added to
  `internal/auth/middleware.go`'s exempt list next to its `pair-status`
  sibling). Both refuse with 409 unless `db.PendingRestore` reports an
  actual staged restore, and the first-boot route carries the same
  5-per-minute rate limit as its `pair-start` sibling.
- **Template**: the first-boot wizard auto-fires the restart on render (a
  Pi kiosk has no shell to press anything from); the manager-driven
  `/tills` flow requires an explicit click. Both always render a visible
  "Restart now" button — the manual fallback never depends on the
  auto-trigger script. A client-side poll against `/healthz` (unbounded,
  switching to a "taking longer than usual" message past ~30s) lands on
  `/login` on 200 or `/` on anything else (so the boot-failure recovery
  screen, ADR-0075, gets a chance to take over instead of leaving the
  operator on a dead screen if the restore itself fails to apply).
- i18n: `tills.pairing.{restarting,restart_now,close_and_reopen,
  nothing_to_restart,restarting_slow}` in all four locales
  (en/ar/fa/tr). `web/help/*/multitill.md` updated; screenshots
  regenerated (`make docs-shots`).

## Independent review — findings and disposition

Reviewed by an Opus subagent (Dev ran on Fable) with instructions to
verify claims against real code, not the design's own reasoning, and to
run everything itself rather than trust prior reports.

| # | Severity | Finding | Disposition |
|---|---|---|---|
| F1 | Blocker | `guard-docs-shots.sh` fails — screenshots stale after this branch's `web/ui`/`internal/pages` changes | **Fixed**: `make docs-shots` regenerated all 100 shots; guard green. Only `ar`/`fa` `multitill.png` and (unrelated, 1-byte PNG-encoder noise) `en/sell.png` actually changed — confirmed visually the pairing "joined" state isn't part of any captured screenshot, so no content changed, only freshness stamps. |
| F2 | Major | `/api/setup/pairing-restart` was an unauthenticated, unconditional, unrated-limited restart primitive — any anonymous LAN device could hold a first-boot till in a restart loop, or restart it before anything was ever staged | **Fixed**: handler now refuses with 409 unless `db.PendingRestore(d.Cfg.DBPath)` is true (guaranteed true on every legitimate call, since `completeJoin` stages the restore strictly before the state flips to "joined"); the setup route now shares `pair-start`'s 5/min rate limiter. Both guards apply to the manager route too. 4 new tests. |
| F3 | Major | Past the ~30s poll bound, the operator was left on a dead screen with no visible way forward — worse, in ADR-0075's boot-failure-recovery case `/healthz` never returns 200 (by design, 503), so the poll always timed out and a retry click 404'd against the recovery mux with zero feedback | **Fixed**: poll is now unbounded (switches to a "taking longer" message past ~30s rather than giving up), treats ANY resolved fetch (200 or not) as "something is answering" and navigates to `/login` or `/` accordingly (`/` correctly hands off to the recovery screen's own real retry UI), and a failed/errored restart request also resumes the poll instead of being a silent no-op. |
| F4 | Minor | The manager-driven `/tills` restart auto-fired on render identically to the first-boot wizard — a configured, possibly-in-use till restarting the instant a pair is approved, no confirmation | **Fixed**: new `autoRestart` template flag, true only for the first-boot flavour; the manager flavour keeps the visible button but requires an explicit click. |
| F5 | Minor | Package doc's safety claim ("selfupdate proves this in production") reads broader than it is — hardware-plugin child processes (`internal/plugins`, no `SysProcAttr`/process-group) are not reparented, cancelled, or signalled by an exec of their parent, and survive/duplicate across a restart | **Doc narrowed** to what's actually verified (lock + socket CLOEXEC release); child-process gap is pre-existing in `selfupdate` too and tracked separately as **ut-docs#1616**. |
| F6 | Scope | The paste-a-code join flow (`/api/sync/join`, `/api/setup/join` in `sync_api.go`) has the identical dead end, not touched by this card | Already found independently by Dev during implementation and filed as **ut-docs#1615** before this review ran; review confirmed the same finding. Correct scope call — the card was specifically `pairing_wait.html`. |

Also independently re-verified rather than taken on faith: the CLOEXEC
release reasoning for the data-directory lock and listening socket (an
`F_GETFD` probe, not just inference from `selfupdate`'s track record); the
`-wal`/`-shm` cleanup in `db.backup.go` that makes the abrupt exec safe for
*this* restore path specifically; that `internal/auth/middleware.go`'s
`exempt()` match is exact-string, not a prefix, so the new exemption
doesn't widen anything; and four separate revert-then-restore TDD checks
(naive synchronous re-exec, ignoring `os.Executable` errors, removing the
auth exemption, and reverting the template) — every one failed with a
specific, real error message, including through the actual auth
middleware rather than a bare mux.

## Verified beyond automated tests

- **Real live process restart**: built the actual `unitill-pos` binary,
  ran it against a temp data dir, hit `POST /api/setup/pairing-restart`
  live, and watched the process genuinely self-restart — same PID, a
  fresh "Universal Till POS starting…" log sequence, `/healthz` recovering
  with 200 after the exec. Also confirmed the manager route answers 401
  without a session against the live server. Killed and cleaned up the
  test process/binary/data dir afterward.
- **Visual check**: rendered the actual "joined" fragment via the real
  Go template (not a hand-built mock) for the supported branch in
  en/fa/ar, the unsupported (close-and-reopen) branch, and the mid-restart
  "restarting" message state; screenshotted each with the real site CSS
  via headless Chromium and inspected them. No overlap, no cut-off text,
  correct RTL right-alignment in fa/ar, button and message coexist
  correctly. This product has no dark mode (`web/public/app.css`'s own
  comment confirms a single fixed light theme), so that check doesn't
  apply.
- No Playwright e2e spec covers the "joined" state
  (`e2e/tests/tills-pairing-layout-1548.spec.ts` doesn't reach it); a
  genuine end-to-end test would need a real two-till pairing handshake,
  disproportionate to this fix. The live single-binary drive above is the
  substitute, and arguably stronger for this specific mechanism (it proves
  the real OS-level process restart, which a Playwright session would lose
  its connection through anyway).

## Explicitly deferred (new Backlog cards, not silently dropped)

- **ut-docs#1613** — `internal/pages/backup_api.go`'s identical
  "restart this till to finish" dead end (found at BA time).
- **ut-docs#1614** — native Windows auto-restart via the
  `WindowController` control channel (ut-docs#882), so Windows desktop
  gets a real one-click restart instead of "close and reopen."
- **ut-docs#1615** — the paste-a-code join flow's identical dead end
  (found by Dev; F6 above).
- **ut-docs#1616** — hardware-plugin child processes surviving/duplicating
  across a `selfupdate`/`procrestart` self-exec restart (found in review;
  F5 above).

## Verdict

**Safe to merge.** All three blocker/major findings (F1/F2/F3) fixed and
re-verified; F4 fixed; F5 addressed via a narrowed doc claim plus a
tracked follow-up; F6 correctly out of scope and already tracked. Full
gate green: `gofmt`, `go build`, `go vet`, `golangci-lint` (0 issues),
`go test` (procrestart/auth/pages, including the 4 new review-driven
tests, full `internal/pages` suite), `guard-i18n.sh`,
`guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-help-topics.sh`,
`guard-compliance-claims.sh`, `guard-docs-shots.sh`.
