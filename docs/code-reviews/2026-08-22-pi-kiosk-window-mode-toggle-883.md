# Pi kiosk: real Settings window-mode toggle (ut-docs#883)

## What shipped

The Settings → Display window-mode selector already existed (#608/#611)
but only ever persisted the chosen mode — it never actually applied
anything to the OS. This card wires the Raspberry Pi headless kiosk
appliance path to a real effect:

- `internal/pages/common/kiosk_window_controller.go` (new):
  `KioskSystemdWindowController`, a `WindowController` implementation that
  drives the real `unitill-kiosk.service` via `sudo -n systemctl <verb>
  unitill-kiosk.service`, bounded by a 30s timeout
  (`exec.CommandContext`). `ApplyMode("kiosk")` → enable+start;
  any other mode → disable+stop, aborting on the first failing verb.
  `ExitToOS()` is a deliberate no-op (no OS desktop exists on the
  headless Pi kiosk — the window-mode toggle itself is the way out).
- `internal/pages/common/window_controller.go`: `WindowController` gains
  `ApplyMode(mode string) error`; `NoopWindowController` gets a no-op
  implementation. The only other implementers repo-wide
  (`recordingWindowController` in tests) were updated to match — the
  desktop shell (`cmd/unitill-desktop`) has its own, unrelated window-mode
  code and does not implement this interface.
- `internal/pages/init.go` / `internal/pages/init_kiosk_detect.go`: `Init`
  selects `KioskSystemdWindowController` when `runtime.GOOS == "linux"`
  AND (`UT_KIOSK=1` is set OR `unitill-kiosk.service`'s unit file is
  present on disk) — the unit-file probe is what makes an *already
  Pi-kiosked box from before this card* pick up the real controller too
  (see Review finding F1 below).
- `internal/pages/settings_page.go`: the `POST /api/settings/window-mode`
  handler now calls `WindowCtl.ApplyMode(mode)` **before** persisting the
  new `WindowMode` — a failure (missing sudoers grant, `systemctl` error)
  surfaces as a 500 and the stored preference never lies about what the
  OS actually did; success logs nothing extra, failure is logged via
  `logging.L().Errorf` for `journalctl -u unitill-pos` diagnosis.
- `packaging/linux/unitill-kiosk-setup.sh`: after the kiosk service unit
  is created+enabled, the script now (a) drops
  `/etc/systemd/system/unitill-pos.service.d/kiosk.conf` setting
  `Environment=UT_KIOSK=1` and restarts `unitill-pos.service`, and (b)
  writes a narrowly-scoped `/etc/sudoers.d/unitill-kiosk` NOPASSWD grant
  for exactly `systemctl {enable,disable,start,stop}
  unitill-kiosk.service` — no wildcard, no other unit — validated with
  `visudo -c` before being installed under its real name.
- `web/locales/{en,fa,tr,ar}.json`: reworded
  `settings.display.window_mode_pending_note` to describe the Pi kiosk's
  real, immediate behaviour instead of the old blanket "not implemented
  anywhere yet" claim.
- `web/help/{en,fa,tr,ar}/display.md`: updated item 8 to describe the Pi
  kiosk toggle's real, immediate (no-restart) behaviour, and warn that
  switching away from "kiosk" closes the only screen on a headless
  appliance (needs another LAN device/SSH to switch back).
- Screenshots regenerated (`make docs-shots`) — no visible layout change,
  only the manifest content-hash for the touched `display` topic (+
  pre-existing drift on `alerts`/`designer`/`translations`, unrelated to
  this diff, picked up by the same "app surface changed" trigger).

## Independent review

Spawned via `Agent` at **Opus** (per this card's `complexity:medium`
routing — Sonnet built it, Opus reviewed it), in an isolated worktree
(`isolation: "worktree"`), against a `WIP: pre-review snapshot` commit.
Full findings are in the agent's report; summarised here.

**Verdict on the first pass: yes-with-fixes-needed-first.** The core
logic (sudoers scoping, verb ordering, interface extension, apply-
before-persist ordering) was confirmed correct — including two
independent revert-then-restore TDD re-verifications
(`TestWindowModeEndpoint_ApplyModeFailureSurfacesAndDoesNotPersist` and
`TestKioskSystemdWindowController_ApplyMode_SurfacesFailure`, both
reverted to their pre-fix code, re-run to confirm the real failing
error, then restored and re-passed). But it found one blocker and five
should-fix items, all fixed in this same branch before merge:

- **F1 (blocker):** `unitill-kiosk-setup.sh`'s own `is_pi_appliance` gate
  deliberately never re-triggers on an upgrade, so every Pi already
  kiosked before this card shipped would keep `UT_KIOSK` unset and the
  toggle would silently do nothing — contradicting the acceptance
  criteria's own named "missing grant surfaces a clear error" scenario
  with the more common "neither UT_KIOSK nor the grant" shape. **Fixed**:
  `Init` also detects an already-installed `unitill-kiosk.service` unit
  file (`internal/pages/init_kiosk_detect.go`,
  `internal/pages/init_kiosk_detect_test.go`), so such a box now
  attempts the real call and gets the intended clear error instead of
  silence.
- **F2 (should-fix):** the sudoers drop-in was written directly to its
  real path before `visudo -c` validated it — a crash mid-write (a first
  boot power cut is realistic) could leave a broken file live. **Fixed**:
  written to a dot-prefixed `mktemp` file inside `/etc/sudoers.d` first
  (inert to sudo's own directory scan before validation), `install -m
  0440`'d to the real path only once `visudo -c` passes.
- **F3 (should-fix):** the whole grant block ran under `set -e` *before*
  the kiosk service was installed, so any failure in it (missing `pos`
  user, no `sudo` package, `visudo` missing) aborted the entire setup —
  a regression from "always get a working kiosk" to "sometimes get
  nothing," contradicting this same script's own established
  "screencast portal not fatal" precedent a few lines up. **Fixed**:
  moved after `systemctl enable unitill-kiosk.service` /
  `set-default`, and every failure path now warns + continues instead of
  `exit 1`.
- **F4 (should-fix):** `sudo systemctl …` had no `-n` (non-interactive)
  flag and no timeout — a missing grant could in principle prompt
  instead of failing fast, and `systemctl start` can genuinely block (this
  script's own comment already warns about a stall on
  `Conflicts=getty@tty1`). **Fixed**: `sudo -n`, `exec.CommandContext`
  with a 30s bound.
- **F5 (should-fix):** the `ApplyMode` error was discarded entirely —
  nothing reached `journalctl -u unitill-pos` for a field diagnosis.
  **Fixed**: `logging.L().Errorf` before the `http.Error`, matching this
  file's own established pattern elsewhere.
- **F6 (should-fix):** the in-app note next to the selector
  (`settings.display.window_mode_pending_note`) still claimed nothing
  works on any platform yet — false for the Pi kiosk (this diff) and
  already false for Linux desktop (#611). **Fixed**: reworded across all
  4 locales.
- **F7 (should-fix, docs):** the manual didn't warn that switching a Pi
  appliance away from "kiosk" closes its only screen with no console
  behind it. **Fixed**: one sentence added, all 4 locales.
- **F8 (nit→should-fix):** `/opt/unitill/pos.env` is a dpkg conffile
  (`config|noreplace`); scripting `UT_KIOSK=1` into it risked a future
  release's conffile prompt or a silent revert on `--force-confnew`.
  **Fixed**: switched to a `unitill-pos.service.d/kiosk.conf` systemd
  drop-in instead — root-owned throughout, idempotent to overwrite, no
  conffile interaction.

Also flagged as **out of scope / accepted**: a product question (should
"fullscreen" on a Pi behave differently from "stop the kiosk"? — matches
the card's own spec, left to the product owner if they want to revisit);
a few nits (stale comments, `UT_KIOSK` parsed with `== "1"` here vs.
`strconv.ParseBool` in `internal/server/server.go` — pre-existing,
unrelated to this diff).

## Verified beyond automated tests

- Live smoke-test of the real binary (not just `httptest`): built
  `unitill-pos`, ran it on this Linux sandbox with `UT_KIOSK=1
  UT_AUTH=off`, and `POST /api/settings/window-mode` with `mode=kiosk`
  and `mode=fullscreen` — both correctly selected
  `KioskSystemdWindowController` (not `NoopWindowController`), attempted
  the real `sudo systemctl …` (which fails on this sandbox, no such
  service/no grant, as expected), and surfaced a clean `500 could not
  apply window mode` — never a panic, never a silent 204. This proves
  the wiring through `pages.Init` end-to-end, which `httptest`-based unit
  tests (which construct `Deps` by hand) cannot reach.
- Sudoers heredoc content manually extracted with a placeholder
  `SYSTEMCTL_BIN` and validated with a real `visudo -c -f` — parses OK.
- `bash -n` on the full rewritten `unitill-kiosk-setup.sh` — syntax OK.
- Real hardware verification (does the sudoers grant/systemd drop-in
  actually work on a physical Pi) is explicitly out of scope for this
  card — tracked separately at ut-docs#21, per the card's own acceptance
  criteria ("cloud-buildable... real hardware verification is #21's
  queue").

## Gate (after all fixes)

- `gofmt -l .` — clean.
- `go build ./...`, `go vet ./...` — clean.
- `go test ./...` — full suite, all packages green (includes
  `internal/pages`, `internal/pages/common`, `packaging`).
- `scripts/ci/guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh` (after
  `make docs-shots`), `guard-help-topics.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh` — all green.

## Explicitly deferred / not in scope

- Real Pi hardware verification (ut-docs#21).
- The desktop-shell live cross-process control channel (ut-docs#882,
  sibling card) — this card is the headless Pi kiosk appliance only.
- A platform-aware (rather than reworded-static) in-app note — the
  simpler static reword (F6) was judged sufficient; a conditional render
  would need new plumbing for marginal benefit.
- The product question flagged by review (should "fullscreen" on a Pi
  differ from "any non-kiosk mode"?) — matches the card's own spec as
  written; left for the product owner if they want to revisit.

## Safe to merge

Yes. All blocker/should-fix findings from the independent Opus review
are fixed in this branch, with real tests (TDD: failing test confirmed,
then fixed, then passing) for every code-level fix, and the full gate is
green.
