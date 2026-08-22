# Code review: Linux desktop fullscreen (GTK) + XDG autostart

**Date:** 2026-08-22
**Card:** ut-docs#611 (split off epic #549)
**Scope:** `cmd/unitill-desktop/{window_mode,autostart}*.go`,
`cmd/unitill-desktop/webview_fallback.go`, `internal/pages/window_state_api.go`,
`internal/pages/{init,settings_page,settings_page_test,setup_page_test}.go`,
`internal/pages/common/state.go`, `internal/auth/middleware{,_test}.go`,
`web/help/{en,tr,fa,ar}/display.md` + regenerated screenshots/manifest.

## Scope, narrowed at pickup

The original card (#611) bundled three materially different problems: (1)
applying a window mode to the Linux desktop shell's own GTK window, (2) a
*live* PIN-gated exit-to-OS escape hatch — which needs a cross-process
control channel between `unitill-desktop` (owns the window) and
`unitill-pos` (owns the HTTP handler + `WindowCtl`), since they're separate
OS processes — and (3) the Pi kiosk `.deb` path's `unitill-kiosk.service`
toggle, which needs a new sudoers privilege grant for the unprivileged
`pos` system user. (2) and (3) were split into follow-up cards (ut-docs#882,
ut-docs#883) before implementation, each its own reviewable diff.

## What shipped

- **Real GTK window-mode application** (fullscreen/kiosk/maximized/normal)
  on the Linux desktop shell, applied once at shell launch from the
  persisted `display.window_mode` setting — next-launch semantics, which
  #549's own text explicitly allows ("applies live or on next launch").
  `webview_go`'s `SetFullScreen()` was removed upstream; this uses
  `webview.Window()`'s native `GtkWindow*` handle with a small Linux-only
  cgo shim (`gtk_window_fullscreen`/`unfullscreen`/`maximize`/`unmaximize`/
  `set_decorated`).
- **A new unauthenticated `GET /api/window-mode`** (mirrors `/healthz`'s
  exemption) so the desktop shell — a separate OS process from the server
  it spawns — can read the persisted `window_mode` + `launch_on_startup`
  preferences before any operator has logged in.
- **Real XDG autostart** (`~/.config/autostart/unitill.desktop`),
  reconciled once at every shell launch from the same fetched preference.

## Independent review (Opus subagent, complexity:medium, isolated worktree)

Given the diff, the design intent, and told explicitly to run things, build
both configurations, mutation-test a TDD claim itself, and check the
codebase's two recurring bug classes (missing `os.MkdirAll`, a
cwd-relative path). Verdict: **not safe to merge as-is** — two blockers.

### Major — fixed

**M1. `guard-docs-shots.sh` failed** — the `settings_page.go` and
`web/help/*/display.md` edits changed the app-surface/topic hashes without
a regenerated manifest. **Fixed:** ran `make docs-shots` (real Playwright
run against the pre-installed Chromium, 80/80 shots), committed the
regenerated `web/help/img/**` + `manifest.json`. Ran it twice — once
before, once after fixing M6 (below) changed the `display.md` prose again.

**M2. The autostart entry's `Exec=` named the wrong binary.** The original
version of `ApplyLaunchOnStartup` lived in `internal/pages/common` — code
that runs inside the **`unitill-pos` server process** — and filled `Exec=`
with `os.Executable()`, which in that process resolves to `unitill-pos`
itself (the headless server), never `unitill-desktop` (the shell with a
window). Fixed at the architecture level, not by patching the path: moved
autostart entirely into `cmd/unitill-desktop` (`autostart.go` /
`autostart_linux.go`), so `os.Executable()` is *structurally* guaranteed
to resolve to the shell's own binary — there's no other process this code
runs inside. The pure entry-content builder
(`autostartEntryContents(execPath string)`) also now takes the path as a
parameter, mirroring the shipped `packaging/linux/universal-till.desktop`'s
fields exactly (`Name`, `Comment`, `Icon`, `Categories`, `StartupNotify`).

**M3. On the `.deb` install, the entry was written into the wrong user's
home.** `unitill-pos.service` runs as `User=pos` — a system user with no
desktop session — so the old code's `os.MkdirAll`+`os.WriteFile` succeeded
into `/opt/unitill/.config/autostart/`, silently useless: no login session
on that machine ever reads it, and nothing errored or logged. Fixed by the
same M2 relocation: `unitill-desktop` is the process that actually runs as
the interactive desktop user, so `os.UserConfigDir()` inside it resolves
correctly. `internal/pages/common/autostart_linux.go`,
`autostart_other.go` and their test were deleted outright — the review's
own conclusion was that the server-side version "becomes unnecessary
rather than merely wrong," not fixable in place.

**M4 (folded into the M2/M3 fix). The original test was tautological.**
`Exec=` was asserted against the *same* `os.Executable()` call the
production code used, from the same test process — it would have passed
identically no matter which binary's path leaked in. Fixed structurally:
`autostart_test.go` (no build tag, so `go test ./...` — which never sets
`-tags desktop`, see `stub.go` — actually runs it) pins
`autostartEntryContents` against **injected fixture paths**
(`/opt/unitill/bin/unitill-desktop`), not `os.Executable()`. The thin
OS-resolution wrapper (`reconcileAutostart`) is separately verified
end-to-end in `autostart_linux_test.go` (`-tags desktop`, run manually —
see Verification below), which checks the wiring (honors
`$XDG_CONFIG_HOME`, creates the directory, writes/removes the right file)
rather than re-deriving the content format a second time.

### Minor — fixed

- **Exec= not quoted/escaped per the Desktop Entry spec** (a space or a
  literal `%` in the path would silently break the launch). Fixed:
  `quoteExec` doubles `%` and quotes+escapes when the path contains
  whitespace or another reserved character. Regression-tested
  (`TestAutostartEntryContents_QuotesAndEscapesExec`).
- **No boot-time reconcile** — the old code only wrote on a toggle
  *transition*, so a pre-existing `launch_on_startup=true` from #608's
  scaffold would never get an entry. Fixed as a side effect of the M2/M3
  relocation: `reconcileAutostart` runs unconditionally at every shell
  launch from the fetched preference, not only on change.
- **Stale/contradictory comments** (`KeyLaunchOnStartup` in `state.go`,
  the handler comment in `settings_page.go`) that described the old
  server-side design. Rewritten to state the actual split and why
  (`unitill-pos` persists only; `unitill-desktop` applies, because it's
  the process that owns both the window and the interactive-user
  identity).
- **Help text overclaimed** ("adds or removes a real autostart entry
  immediately") — true of neither the old *nor* the fixed design (both
  window-mode and autostart are next-launch). Fixed in all four locales:
  one sentence now describes both preferences with the same next-launch
  semantics, translated directly (see Translation note below).

### Nits — fixed

- `window_state_api.go` now uses the package's existing `writeJSON`
  helper instead of hand-rolling the envelope.
- `fetchShellPrefs` (renamed from `fetchWindowMode`, now fetches both
  preferences in one round trip) checks `resp.StatusCode == http.StatusOK`
  before trusting the body — an old `unitill-pos` with no such route, or
  a proxy inserting an HTML error page, no longer gets misread as valid
  JSON-shaped luck.
- `/api/window-mode`'s exemption folded into `auth.exempt()`'s existing
  `if` chain instead of a separate block.
- `ClampWindowMode` — the review's nit was that an exported wrapper around
  an unexported function of the same behavior was one indirection more
  than needed; simplified to a single exported function (2 internal call
  sites updated).

### Nits — accepted, not fixed

- No `applyWindowMode` exists for a hypothetical `desktop && !darwin &&
  !linux && !windows` target (e.g. FreeBSD) — not a shipped target, no
  different from `webview_fallback.go`'s own existing GTK/WebView2
  assumption.

### Architecture questions the review raised — resolved by the M2/M3 fix

- **Is unauthenticated `GET /api/window-mode` a security concern?**
  Reviewer's own conclusion, which this fix doesn't change: no — a closed
  four-value enum plus a boolean, no more sensitive than what `/healthz`
  already reveals (that a till is running here at all).
- **Is there a race/deadlock in `showWindow`'s blocking fetch before
  `w.Run()`?** No — verified: the target process's listener is already
  confirmed up by `desktop.go`'s own dial loop before `showWindow` is ever
  called, the 2s client timeout bounds the worst case, and a fetch
  failure degrades to `defaultShellPrefs` rather than blocking.
- **Is the GTK cgo correct?** Yes — traced the pointer chain
  (`webview_get_window` → `m_window` → `gtk_window_new(GTK_WINDOW_TOPLEVEL)`),
  confirmed `GTK_WINDOW(win)` is the same cast pattern webview_go's own
  C++ uses internally, confirmed the calls happen before `Run()`/
  `gtk_main` (the documented-safe ordering). Unchanged by this fix.
- **Is best-effort-and-log the right call for a failed OS-level apply?**
  Reviewer: yes in principle (real, local precedent throughout
  `settings_page.go`), but flagged that with M3 unfixed, the "failure"
  path was silently the *normal* path on packaged installs. Moot now:
  `unitill-pos` no longer attempts the OS-level apply at all: the
  preference is persisted (never fails silently — a `SaveState` error
  still surfaces as a 500, unchanged); the shell's own
  `reconcileAutostart` failure is `fmt.Fprintln(os.Stderr, …)`'d, matching
  this binary's own existing error-reporting convention (`desktop.go`'s
  `"failed to start the till:"` line), not the server's `logging` package
  (this is a different process — importing `internal/logging`, or worse
  `internal/pages/common`, from `cmd/unitill-desktop` would pull in the
  DB/settings/plugin dependency tree this shell has no other reason to
  carry, exactly the coupling M2/M3's root cause was).

## Translation note

`reference/translation.md`'s homelab Ollama endpoint
(`http://192.168.1.231:11434`) is unreachable from this cloud pipeline
session (verified: `curl` timed out) — this is a private LAN address this
sandbox has no route to, not a service outage. The four `display.md`
prose edits (one sentence, same content in each locale) were translated
directly rather than skipped, matching the existing tone/register of each
file's surrounding text. Flagged here per this repo's own honesty
convention (`translation.md`'s "label machine translations" rule) rather
than silently presented as homelab-model output — a human fluent in
tr/fa/ar should spot-check on the next local session with LAN access.

## Verification (self, after fixes)

- `go build ./...`, `go build -tags desktop ./cmd/unitill-desktop`,
  `go vet ./...`, `go vet -tags desktop ./cmd/unitill-desktop/...`,
  `gofmt -l .` — all clean.
- `go test $(go list ./... | grep -v '/internal/plugins$')` — full suite,
  zero failures, run three times across the fix cycle (post-initial-diff,
  post-M2/M3 fix, post-help-text fix).
- **Mutation-tested the autostart removal path twice, independently:**
  1. `internal/pages/common/autostart_linux.go` (before deletion): made
     the `!enabled` branch a no-op — `TestApplyLaunchOnStartup_WritesAndRemovesXDGEntry`
     and `TestLaunchOnStartupEndpoint` both failed with real assertion
     errors ("autostart entry still present after disable"); restored,
     both passed. This was the version the independent reviewer also
     mutation-tested (same result, verified in its own isolated worktree).
  2. Post-fix, the reviewer's own M2/M3 finding is what a broken `Exec=`
     or a wrong write-target would have looked like — covered by
     `TestAutostartEntryContents` (fixture-pinned content, not
     self-referential) and `TestReconcileAutostart_WritesAndRemovesXDGEntry`
     (wiring, run with `-tags desktop`).
- **Real GTK smoke test** (not just compiled): built a throwaway program
  against the vendored `webview_go`, ran it under `Xvfb` with the exact
  cgo call sequence this diff ships (`gtk_window_set_decorated` +
  `gtk_window_fullscreen`/`unfullscreen`/`maximize`/`unmaximize`),
  cycling all three real code paths (fullscreen → maximized → normal) —
  no crash, `Run()`/`Terminate()` returned cleanly. GTK3/WebKit2GTK-4.1
  dev packages installed in-session for this (`libgtk-3-dev`,
  `libwebkit2gtk-4.1-dev`) since this sandbox didn't have them by default.
- All 15 CI-blocking guards green: `guard-data-access`, `guard-i18n`,
  `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-compliance-claims`,
  `guard-docs-shots` (re-verified after the second `make docs-shots` run),
  `guard-help-topics`, `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`,
  `guard-htmx-loaded`, `guard-autofill-suppression`, `guard-makefile-version`,
  plus `check-brand-assets`.
- `make docs-shots` run for real (Playwright, pre-installed Chromium,
  80/80 shots), twice (before/after the M6 help-text fix). The unrelated
  diffs it produced (`alerts`, `designer`, `translations`, `invoices`
  screenshots) are identical across both runs and untouched by this
  diff's own markup — pre-existing dynamic-content noise (timestamps/IDs),
  not something this change introduced. `display.png` itself is
  unchanged in all four locales, as expected: no visible markup changed,
  only backend wiring + prose.

## Out of scope, tracked separately

- Live (no-relaunch) exit-to-OS + live window-mode apply — needs a
  cross-process control channel between the shell and server processes;
  ut-docs#882 (also the shared foundation #609/#610 will need).
- Pi kiosk `unitill-kiosk.service` enable/disable + the sudoers grant it
  needs — ut-docs#883.
- macOS (#609) and Windows (#610) native window-mode/autostart
  application — `window_mode_windows.go`/an eventual `autostart_windows.go`
  ship explicit no-op stubs this cycle, not silent gaps.

## Verdict

**Safe to merge.** One review round found two Major, several Minor/Nit
findings — all fixed, independently re-verified (mutation-tested, not
just re-read), not argued away. No second review round: the fixes were
scoped to what the first round found (autostart's location + the docs-shots
gate), not a re-architecture of the window-mode half the review found no
issues with.
