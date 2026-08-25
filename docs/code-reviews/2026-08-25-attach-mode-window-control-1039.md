# Code review — exit-to-OS on a `.deb` install, and refusing kiosk without it (ut-docs#1039)

- **Date**: 2026-08-25
- **Card**: ut-docs#1039 (p1, `complexity:hard`, `security`, `source:user`)
- **Design**: ADR-0064 — the shell's window control is server-authoritative
  and polled (`ut-docs/adr/0064-*.md`), plus its same-day amendment
- **Branch**: `fix/1039-attach-mode-window-control`
- **Review**: independent subagent, Opus, in its own git worktree, with no
  sight of the implementing session's reasoning. One round, plus this
  verification pass.

## What the change does

On every `.deb`/Raspberry-Pi-with-desktop install, `unitill-pos` runs as a
systemd service under system user `pos` while `unitill-desktop` starts
later as the interactive desktop user, attaching to a server it did not
spawn. It therefore cannot hand that server
`UT_DESKTOP_CONTROL_ADDR` (ut-docs#882's env handoff), so
`Deps.WindowCtl` resolved to `NoopWindowController`: the PIN-gated
`POST /api/settings/exit-to-os` checked the manager PIN, wrote an audit
row, called a no-op and answered `204 Exited to OS.`

The *entering* half travelled a different, working route — the shell's own
read of `GET /api/window-mode` into `applyWindowMode`, which really calls
`gtk_window_fullscreen()` and `gtk_window_set_decorated(FALSE)`. Lockdown
engaged, escape did not, on a touchscreen with no keyboard.
`flagsForWindowMode` maps **both** `kiosk` and `fullscreen` to undecorated
fullscreen, so both modes carried the trap.

The fix inverts the channel per ADR-0064: the server is the sole authority
for the window mode and the shell long-polls it, so all control traffic is
shell → server — no new inbound surface, and no secret shared across the
two OS users. Chrome-hiding modes are served only to a client that is
genuinely able to leave them.

## Round 1 findings — one blocker (two halves), six should-fixes, three nits

**BLOCKER 1 — the Pi kiosk appliance still fabricated success.**
`KioskSystemdWindowController.ExitToOS` was left as `return nil`, so on a
shipping appliance a correct manager PIN took the *success* path: `204`,
"Exited to OS.", **and an `exit_to_os` audit row**, while cage+chromium
stayed fullscreen. ADR-0064 Decision 4 binds this change to fixing exactly
that, and `web/help/en/display.md` already documented the opposite of what
the UI did — the product and its own manual contradicting each other, on
the one card whose load-bearing criterion is that the product never lies
to the operator.
*Fixed*: `ExitToOS` returns `ErrNoOSDesktop`; the handler maps it to `503`
with its own operator string, in all four locales.

**BLOCKER 2 — the fail-closed guarantee was correlated, not bound.** The
downgrade keyed only on the client sending `control=live`, never on
whether `Deps.WindowCtl` is the controller that actually consumes
`Deps.Shell`. Where `newWindowController` returns the Pi kiosk controller,
nothing writes the channel and `ExitToOS` is a no-op — yet a real GTK
shell advertising `control=live` was still served `kiosk` and really
fullscreened and undecorated the window. **The reviewer rebuilt the
ut-docs#1039 trap and proved it with a probe test:**

```
mode served to a control=live shell while WindowCtl is the Pi kiosk controller: "kiosk"
exit-to-os status = 204, exit_to_os audit rows = 1, live mode after exit = "kiosk"
```

Reachable, not theoretical: the kiosk detection is a sticky file-presence
probe deliberately never re-evaluated; the documented undo
(`systemctl disable --now unitill-kiosk`) **leaves the unit file on
disk**; that script supports desktop images and non-Pi Debian boxes; and
the persisted mode can reach `kiosk` with no `ApplyMode` call at all via a
cloud `set_setting` directive or replica drift, which the channel then
re-seeds at every server start.
*Fixed*: `ShellChannel.MarkExitPath()`, called **only** from
`NewShellPollWindowController`, and the downgrade now requires
`live && sh.IsExitPath()`. The invariant is written down at the decision
point and covered by a test that fails if either half is removed.

**SF3 — "Nothing changed." was false, and a real lockdown break went
unaudited.** The controller sets the live mode to `normal` *before*
`WaitApplied`, so on ack timeout the operator saw "…can't be closed from
here. Nothing changed." while the state had in fact changed and the shell
would leave kiosk moments later. Proved: `live mode = "normal", rev = 2`
after the "nothing changed" failure. *Fixed*: a distinct
`exit_to_os_not_confirmed` string, and an audit row with
`{"confirmed": false}` — the lockdown break happened and it was
authorised.

**SF4 — the acknowledgement confirmed a dispatch, not an applied window.**
`lastApplied` advanced right after `w.Dispatch(...)` returned, i.e. once
the call was *queued* onto the GTK thread. ADR-0064 Decision 2 promises
success only if the window really came back. *Fixed*: `lastApplied` now
advances from inside the dispatched closure, so a wedged GTK loop simply
never acks and the honest not-confirmed path fires.

**SF5 — no shell-side watchdog.** Poll errors never ended the loop but
never downgraded either, so a crash-looping `unitill-pos` after an
`apt upgrade` left the shell fullscreen and undecorated over a
"can't connect" page — on a keyboardless touchscreen, with no exit. The
ADR's "the failure mode is a normal window, never a sealed one" held only
*at next launch*. *Fixed*: consecutive-failure duration is tracked, and
once it outlasts the attached window while the current mode is
chrome-hiding, the shell applies `normal` itself.

**SF6 — `control=live` was accepted from any LAN client.** The default
bind is `:8080` (the loopback line in `pos.env.example` is commented out),
the endpoint is `auth.exempt`, and the server sets no timeouts and no
concurrency cap. A LAN host could park unbounded 25s long polls (one
goroutine + connection each — newly created exposure, since this endpoint
used to answer instantly), hold `Attached()` true on a till with no shell
at all, and — the direction the ADR had *not* accepted — spam
`applied=kiosk` to **suppress a genuine acknowledgement**, turning a real
exit into "can't be reached, nothing changed". *Fixed*: `control=live` is
honoured only from a loopback peer (the shell is always same-host, so this
costs nothing), and parked polls are capped with a try-acquire semaphore
that answers immediately rather than erroring when full.

**SF7 — a server restart re-locked a window the operator had just
escaped.** Exit-to-OS deliberately leaves the persisted preference at
`kiosk`, and the channel seeds from that at start; with
`Restart=always, RestartSec=3` any crash or upgrade would slam a
still-running shell back into undecorated fullscreen. *Fixed* by naming
whose fact each thing is — the attached shell is the authority on what the
window currently **is**, the persisted setting is what it should be at
*shell* launch — via a one-shot-per-boot `AdoptIfUntouched`. It refuses to
adopt *into* a chrome-hiding mode, so adoption can only ever mean "keep
what the window already is", never an instruction to enter one.

**SF8 — the "no desktop shell attached" note was false on a Pi
appliance**, where kiosk is real and there is no shell by design. The
previous handoff had accepted this as cosmetic; the reviewer disagreed and
was right, on a card about not lying to the operator. *Fixed*: the
controller topology is plumbed through so the Display section says the
true thing in all three cases.

**SF9 — `rederiveSettings` never touched the channel**, so a cloud
directive or replica drift left Settings showing kiosk selected with a
shell attached and no warning while the window stayed normal. *Fixed*
(closes ut-docs#1058).

**NIT10** — a test comment claimed the `wait=` clamp was covered when the
test only asserted that a stale `since` returns fast; the clamp had zero
coverage. Fixed with a testable helper and a numeric assertion, plus the
cross-binary `shellPollWaitSeconds <= ShellPollMaxWait` contract pinned
from a test, following `TestEnvVarsMatchCommonPackage`'s precedent.
**NIT11** — `NoteSeen` broadcast on the shared channel, waking every
parked waiter per heartbeat; split into separate mode-change and ack
broadcasts. **NIT12** — the status line is render-time only and goes
stale; deliberately out of scope, filed as ut-docs#1060.

## What the review verified rather than took on trust

- **The TDD claims are real.** Reverting the downgrade made
  `TestWindowStateAPI_FailClosedDowngradesChromeHidingForPlainClients`
  fail with `plain client window_mode = "kiosk", want normal`; reverting
  the wiring made `TestAttachModeWindowControllerIsReal` fail with
  `attach mode wired NoopWindowController`. The reviewer also deleted the
  latter's concrete-type assertion and re-ran it, confirming the
  behavioural assertions catch the regression independently.
- **Concurrency.** `go test ./internal/pages/... ./cmd/unitill-desktop/...
  -race -count=2` — zero data races. `ShellChannel`'s broadcast has no
  lost wakeups (the changed-channel is captured under the same mutex as
  the state check), writers never block, `rev != since` cannot skip an
  intermediate change, and a client disconnect releases a parked handler.
- **Guards**: `guard-i18n` (all locales match en.json), `guard-data-access`,
  `guard-help-topics`, `guard-docs-shots`, `guard-kiosk-engine`,
  `guard-htmx-loaded`, `guard-autofill-suppression`, `guard-emoji-font` —
  all pass; `gofmt`, `go vet`, `go build ./...` clean.
- **Pre-existing failures, not from this card**: the two ut-docs#390
  replica-banner tests fail on macOS only (they gate on a
  `runtime.GOOS`-derived value) and fail identically on the merge base.
  Filed as ut-docs#1057.

## Not verified from this machine, stated plainly

There is **no GTK window and no Raspberry Pi here**, and the
`desktop && linux` files do not compile on macOS (no cross cgo/GTK
toolchain) — they are built only by `release.yml` on a native ARM runner,
not by per-PR CI. So the shell-side changes (the watchdog, the
dispatched-closure ack, the linux-only capability flag) are reviewed and
unit-tested but **have not been run against a real GTK window**, and
nobody has driven a real till into kiosk and back out on hardware. That
verification is owed on the test Pi via an arm64 `.deb`, and the card is
not fully closed until it happens.

## Verdict

Both blocker halves fixed at the root — the appliance now reports the
truth, and "the window is locked down" and "the exit works" are bound to
one signal rather than two correlated ones. Merging on that basis, with
the on-hardware verification tracked as the remaining acceptance
criterion.
