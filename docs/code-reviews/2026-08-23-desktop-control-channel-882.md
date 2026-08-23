# Code review: desktop cross-process window control channel

**Date:** 2026-08-23
**Card:** ut-docs#882 (split off #611, epic #549)
**Scope:** `cmd/unitill-desktop/{control,desktop,webview_fallback,webkit_darwin,
window_mode_linux}.go` (+ new `control_test.go`),
`internal/pages/common/http_window_controller.go` (+ test),
`internal/pages/{init,settings_page}.go`, `internal/pages/common/window_controller.go`,
`web/help/{en,tr,fa,ar}/display.md` + regenerated screenshots/manifest.

## What shipped

A minimal, generic cross-process control channel between `unitill-desktop`
(owns the native GTK/WebView2/Cocoa window) and the `unitill-pos` child it
spawns (owns the PIN-gated `POST /api/settings/exit-to-os` HTTP handler):

- `unitill-desktop` starts a loopback-only (`127.0.0.1:0`) HTTP listener
  before spawning its child, and hands the child the address **and a
  random bearer token** via `UT_DESKTOP_CONTROL_ADDR`/`UT_DESKTOP_CONTROL_TOKEN`
  (mirroring the existing `UT_LISTEN_ADDR` pattern).
- A new `common.HTTPWindowController` on the `unitill-pos` side implements
  the existing `WindowController` interface by calling that channel with
  the token in a header — used only when the env var is present; falls
  back to `NoopWindowController` otherwise (browser access, headless, a
  shell that predates this card).
- `webview_fallback.go` (Linux/Windows) wires real handlers that dispatch
  onto `webview_go`'s UI thread — real, end-to-end on Linux (`applyWindowMode`'s
  GTK calls); inert on Windows today since `applyWindowMode` there is
  still #610's own no-op stub.
- `webkit_darwin.go` (macOS) wires explicit no-op handlers rather than
  leaving the channel unwired — a live toggle there is accepted (204,
  matching today's persist-only behavior) instead of regressing into an
  error. Real Cocoa wiring stays #609's own scope.

## Independent review (Opus subagent, complexity:medium, isolated worktree)

Given the diff, the acceptance criteria, and told explicitly to run
things, mutation-test a TDD claim itself, and check the codebase's two
recurring bug classes. Verdict: **not safe to merge as-is** — 3 blockers.

### Major — fixed

**M1. Browser-fallback path (`webview.New` returns nil, e.g. Windows
without the WebView2 runtime) left the control channel's ops unset while
`ctl` was still non-nil** — every control call then 503'd, and
`HTTPWindowController` turned that into a 500 for the operator. Before
this card that path returned 204 (`NoopWindowController`). The doc
comment on those exact lines asserted the opposite ("ctl is nil in that
case") and was factually wrong. **Fixed:** that branch now wires explicit
no-op ops (same pattern as `webkit_darwin.go`) before returning, and the
comment corrected. Also moved `SetOps` on the happy path to run
immediately after the window is created, ahead of the network-bound
`fetchShellPrefs`/`reconcileAutostart` calls, shrinking the window where a
toggle could still see `ops == nil`.

**M2. The control channel was completely unauthenticated** — proven live
against the real GTK shell under Xvfb: a bare `curl -X POST
http://127.0.0.1:<port>/exit-to-os` (port recovered from
`/proc/<pid>/environ`, exactly how the review found it) returned 204 with
no session, no PIN, no token — a second, PIN-free path to the exact effect
(`applyWindowMode(w, "normal")`, i.e. leaving kiosk) the PIN gate exists to
block. An `application/x-www-form-urlencoded` POST is also a CORS *simple
request* (no preflight), so browser-reachability wasn't ruled out either.
**Fixed:** `newControlServer` now mints a 32-byte `crypto/rand` bearer
token, handed to the child via `UT_DESKTOP_CONTROL_TOKEN`; both handlers
require it in an `X-UT-Control-Token` header (constant-time compare) and
403 otherwise; any request carrying a non-empty `Origin` header is also
403'd regardless of token validity (the real Go client never sends one).
This is a second, independent layer alongside the PIN check — not a
replacement for it: the PIN check still stays entirely inside
`unitill-pos`'s existing handler (AC #4 unchanged), the token only proves
the caller is the process this shell actually spawned.

**M3. The user manual (`web/help/{en,tr,fa,ar}/display.md`, item 8)
contradicted the shipped feature** — it said window-mode/autostart "applies
the next time you start the till" and that "Exit to OS window… doesn't yet
do anything on any platform," both false on the Linux desktop shell as of
this card, the second directly denying the card's headline acceptance
criterion. CLAUDE.md's standing rule (ut-docs#324) requires this in the
same branch; CI never catches prose (`guard-help-topics.sh`/
`guard-docs-shots.sh` only check routes and hashes). **Fixed:** rewrote
item 8 in all four locales — Linux desktop's window-mode selector and Exit
to OS window are now both described as live/immediate, autostart still
next-launch, Pi kiosk's Exit to OS window explicitly a permanent no-op (no
desktop to leave), macOS/Windows still scaffolding but now accurately
described as "accepts the PIN, nothing visibly changes yet" rather than
"doesn't do anything." `fa`/`tr`/`ar` translated directly (the homelab
Ollama endpoint, `http://192.168.1.231:11434`, is unreachable from this
cloud pipeline session — connection timeout, verified — same finding the
2026-08-22 #611 review made) rather than skipped, matching this repo's
`translation.md` honesty convention; a human fluent in those languages
should spot-check on a session with LAN access. Re-ran `make docs-shots`
(topic markdown hash changed) — no visible markup changed, prose only.

### Minor — fixed

- **m1 (use-after-free).** `Dispatch` onto the webview after `w.Destroy()`
  was a possible race: `ctl.Close()` used to be deferred in `main()`,
  which — LIFO — ran *after* `w.Destroy()` (deferred inside `showWindow`,
  which returns before `main()`'s own defers fire). A request landing in
  that gap would dispatch onto an already-freed handle. **Fixed:**
  `ctl.Close()` is now deferred inside `showWindow` itself, registered
  immediately after `defer w.Destroy()` — LIFO now runs it *first*,
  draining in-flight requests before the window is destroyed. Applied to
  both `webview_fallback.go` branches and `webkit_darwin.go`.
- **m2 (no mode validation).** `handleApplyMode` forwarded any `mode`
  value including empty/garbage, silently degrading to "normal" (un-
  fullscreening the till) via `flagsForWindowMode`'s own safe default.
  **Fixed:** allowlist check (`fullscreen|kiosk|maximized|normal`) in the
  control server itself, 400 otherwise — re-validates rather than trusting
  `unitill-pos` already did, since this is its own network-facing
  boundary (CLAUDE.md's "validate all external input").
- **m3.** `init.go`'s comment claimed a live call on macOS/Windows
  "degrades to a clear error" — the opposite of what `webkit_darwin.go`
  actually does (explicit no-op, 204). Fixed to match.
- **m4 (no compiler-enforced env-var contract).** Two different binaries'
  packages hold byte-identical string literals with nothing to catch a
  typo. **Fixed:** `TestEnvVarsMatchCommonPackage` in
  `cmd/unitill-desktop` imports `internal/pages/common` and asserts both
  pairs match — same pattern this ecosystem already uses for `ut-cloud`'s
  `CanonicalManifest` mirror test.
- **m5 (client discarded the server's error body).** `errNoOps`'s
  descriptive "desktop shell window not ready" was thrown away by
  `HTTPWindowController.post`, and the exit-to-os handler didn't log at
  all. **Fixed:** `post` now folds a bounded (256-byte) response-body
  prefix into the returned error; added a `logging.L().Errorf` call to the
  exit-to-os handler (window-mode's already had one).
- **m6 (misleading "never lies" comment).** `settings_page.go`'s
  window-mode handler comment asserted persistence never lies about what
  the OS did — true for the synchronous `KioskSystemdWindowController`,
  only best-effort for the new `HTTPWindowController` (`ApplyMode` always
  returns nil; the native call is fire-and-forget). Comment now says so
  explicitly.
- **m7 (unauthenticated listener with no legitimate client in attach
  mode).** Resolved as a side effect of M2 — the attach-mode listener is
  now token-gated like every other instance.

### Nits — fixed

- **n1.** Added `ReadHeaderTimeout` to the control server's `http.Server`
  (gosec G112 / Slowloris hygiene; loopback-only so low real impact).
- **n2 (stale env inheritance).** If a bind failure meant `ctl == nil`, a
  pre-existing `UT_DESKTOP_CONTROL_ADDR`/`_TOKEN` already in this
  process's own environment would have leaked through to the child
  unfiltered. **Fixed:** `filterEnv` strips both names from `os.Environ()`
  before conditionally re-adding fresh values.
- **n5 (mutex genuinely untested).** The review's own mutation test showed
  `SetOps`/`currentOps`'s mutex is load-bearing but nothing in the
  committed suite would have caught its removal. **Fixed:**
  `TestControlServer_SetOpsConcurrentAccessIsRaceFree` exercises `SetOps`
  concurrently with real HTTP requests under `-race`.

### Nits — accepted, not fixed

- **n3.** `cmd/unitill-desktop/README.md` doesn't mention the control
  channel — it also omits #611's window-mode work, matching existing
  looseness; not this card's job to fix on its own.
- **n4.** `TestControlServer_LoopbackOnly` asserts on the `Addr()` string
  rather than a real off-host connection attempt. Adequate per the
  review's own mutation test (catches the mutations that matter); the
  live end-to-end pass below additionally confirmed real off-loopback
  unreachability.
- **n6.** The two `http.Error` strings the operator now sees more often
  ("could not apply window mode" / "could not exit to OS") are still raw
  untranslated English — a known, already-tracked long tail (ut-docs#316),
  not introduced by this card.

## Verification (self, after fixes)

- `gofmt -l .`, `go build ./...`, `go vet ./...` — clean.
- `go build -tags desktop ./cmd/unitill-desktop`,
  `go vet -tags desktop ./cmd/unitill-desktop/...` — clean (GTK3/
  WebKit2GTK-4.1 dev packages installed in-session, same as the #611
  review).
- `go test $(go list ./... | grep -v '/internal/plugins$')` — full suite,
  zero failures.
- `go test -race ./cmd/unitill-desktop/... ./internal/pages/common/...` —
  clean, including the new concurrent `SetOps` test.
- All 15 CI-blocking guards + `check-brand-assets.sh` green, including
  `guard-i18n` (locales still match `en.json`'s key set — this card added
  no new keys, only existing-topic prose) and `guard-compliance-claims`.
- **`guard-docs-shots.sh`**: `make docs-shots` re-run after the M3 prose
  fix (topic markdown hash tracked separately from app-surface hash);
  84/84 real Playwright shots against the pre-installed Chromium; guard
  green. `display.png` itself unchanged in all four locales (no markup
  changed) — the touched PNGs (`alerts`/`designer`/`invoices`/
  `translations`/`sell`, pixel-diffed by the reviewer down to a
  wall-clock timestamp and encoder-noise byte deltas) are the same
  dynamic-content set the 2026-08-22 #611 review already documented as
  pre-existing noise, unrelated to this diff.
- **Real GTK smoke test, twice** (before and after the fixes), built and
  run under `Xvfb` end-to-end with a real seeded manager PIN:
  - Before the fix: a bare, tokenless `curl -X POST .../exit-to-os`
    against the recovered control-channel port succeeded (204) — the M2
    vulnerability, reproduced live, not just reasoned about.
  - After the fix: the same bare request → **403**; a request with the
    correct token but an `Origin` header → **403**; the correct token
    alone → **204**; an invalid `mode` → **400**; the real
    `POST /api/settings/exit-to-os` with the seeded manager PIN through
    `unitill-pos` → **204**, live window-mode toggle through Settings →
    **204** — the actual channel this card exists to build, proven
    end-to-end, not just unit-tested. Both processes stayed alive and
    responsive through all of it (no use-after-free crash from the M1/m1
    fixes).
  - Off-loopback reachability re-confirmed refused (`connection refused`)
    after the fixes, same as before.

## Verdict

**Safe to merge.** One review round found 3 Major + 7 Minor + non-blocking
nits; all Majors and Minors fixed and independently re-verified (real live
requests against a running shell under Xvfb, not just re-reading the
diff), not argued away. No second review round: every fix was scoped to
what the first round found — the security fix, the fallback-path fix, and
the manual prose — not a re-architecture of the channel design itself,
which the review found sound.
