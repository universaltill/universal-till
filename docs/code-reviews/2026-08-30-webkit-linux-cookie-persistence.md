# Code review — Linux WebKit cookie jar not persisted across restarts (ut-docs#1233)

- **Date:** 2026-08-30
- **Branch:** `fix/1233-webkit-linux-cookie-persistence`
- **Reviewer:** independent reviewer (fresh-context Opus, this pipeline's
  `complexity:medium` review tier), same checkout (no worktree isolation
  used for this review — see Housekeeping below).
- **Verdict: SAFE TO MERGE.** One blocking finding (F1), fixed. One
  recommended hardening (F2), applied. Two optional guard/doc
  improvements (F6/F7), applied. Everything below reflects the fixed
  state — the review ran against the pre-fix diff, then all four were
  applied afterward.

## What shipped

`cmd/unitill-desktop`'s Linux build (the vendored `webview_go` GTK/WebKit2
backend, `internal/thirdparty/webview_go/libs/webview/include/webview.h:1258`)
calls plain `webkit_web_view_new()`, which attaches to the process-wide
default `WebKitWebContext`. In the webkit2gtk-4.1 API line, that context's
cookie manager has no persistent storage configured unless the app
explicitly calls `webkit_cookie_manager_set_persistent_storage()` — the
jar is in-memory only and dies with the process. Two cookies the server
explicitly asks the client to keep are affected: `ut_lang`
(`internal/httpx/httpx.go`, `MaxAge: 31536000`) and the login session
`ut_session` (`internal/pages/auth_page.go`, `MaxAge: auth.SessionTTL.Seconds()`
— 12h). Every `unitill-desktop` restart/reboot silently discarded both,
reverting the kiosk to the shop's default locale and forcing a re-login —
not new behavior the fix introduces, but the server's own already-declared
cookie lifetimes finally being honored on Linux.

The fix, split the same way this package's `window_mode.go`/
`window_mode_linux.go` already are (pure logic under a minimal build tag,
actually exercised by `go test ./...`; a thin cgo wrapper behind the real
`desktop` tag):

- `webkit_datadir_linux.go` (`//go:build linux`, no cgo) — resolves
  `$XDG_DATA_HOME/universal-till/webkit` (fallback
  `~/.local/share/universal-till/webkit`), honoring `$XDG_DATA_HOME` the
  same way `autostart_linux.go` honors `$XDG_CONFIG_HOME`.
- `webkit_linux.go` (`//go:build desktop && linux`, cgo against
  `gtk+-3.0 webkit2gtk-4.1` per ADR-0028) — `init()` overrides a new
  `setupPersistentCookies` hook (declared as a no-op in
  `webview_fallback.go`) with `linuxPersistCookies()`, which resolves the
  data dir, `os.MkdirAll(dir, 0o700)`, and calls a small C helper that
  `gtk_init_check()`s, then points the default context's cookie manager at
  a SQLite file via `webkit_cookie_manager_set_persistent_storage(...,
  WEBKIT_COOKIE_PERSISTENT_STORAGE_SQLITE)`.
- `webview_fallback.go` — calls the hook once, right after
  `waitForSafeStartup()` and before `webview.New(false)`, logging and
  continuing (not failing) on error.

Persists the **whole** jar, not just `ut_lang` — deliberate, not an
oversight (see Findings, F4).

## Independent review — what was checked

- **Gates, all real output, all green (re-run after the fixes, not just
  before):** `gofmt -l .` (empty), `go build ./...`, `go vet ./...`,
  `go test ./...` (whole repo), `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-webkit-version.sh` (including its
  own regression test, `guard-webkit-version_test.sh`), `guard-help-topics.sh`,
  `guard-kiosk-launch-flags.sh`.
- **TDD claim independently re-verified twice** — once pre-fix, once
  post-rename (F1): removing `webkit_datadir_linux.go` breaks the build
  (`undefined: webkitDataDir`); restoring it passes. Three separate
  mutations (swap the fallback branch's directory name, ignore
  `$XDG_DATA_HOME`, drop the `webkit` subdirectory) were each confirmed to
  make the corresponding test fail — these are real pins, not
  tautologies.
- **Root cause re-derived independently** against the vendored engine
  source, not taken on the issue's word.
- **cgo/C correctness** — the highest-risk part, since this sandbox has no
  `gtk+-3.0`/`webkit2gtk-4.1` dev headers and neither author nor reviewer
  could compile it locally. Checked function signatures, the enum name,
  and the header include against the vendored `webview.h`'s own use of the
  same include for the same pkg-config target. `webkit_web_context_get_cookie_manager`
  is confirmed still the documented 4.1-line accessor (removed only in the
  unrelated API 6.0/`WebKitNetworkSession` line this product doesn't
  target). The actual compile/link check happens for real on CI's
  `desktop-shell` job (`ubuntu-latest`, installs
  `libgtk-3-dev libwebkit2gtk-4.1-dev`, runs
  `go build -tags desktop ./cmd/unitill-desktop` and
  `go vet -tags desktop ./cmd/unitill-desktop`) — a genuinely stronger bar
  than most of this package's siblings get pre-merge, and the gate this PR
  depends on for that specific risk.
- **Thread/lifecycle correctness** — `desktop.go`'s `init()` pins the main
  goroutine to the OS thread (`runtime.LockOSThread()`); the hook is
  called exactly once, on that thread, strictly before the first
  `webview.New()`/`webkit_web_view_new()` at both of `showWindow`'s two
  call sites; the `var` + `init()` override pattern is the same shape
  `window_mode.go`/`window_mode_linux.go` already use, not a new
  convention.
- **ADR check** — read `ut-docs/adr/` in full; ADR-0028 (webkit2gtk-4.1) is
  the only one bearing on this diff, and it's honored. No ADR governs
  cookie jars, session persistence, or kiosk lock-screen lifecycle.
- **No scope creep** — diff stays inside `cmd/unitill-desktop` (plus the
  guard script and its own README), no SQL/money/i18n/plugin/kiosk-engine
  surface touched.
- **No secret leakage** — the one new log line prints a path and a wrapped
  error, never cookie contents.
- **Customer-facing leak check** — `self_order.html`/`self_order_shop.html`
  have no `?lang=` switcher, so an anonymous self-order customer can't pin
  `ut_lang` permanently via the now-shared jar.

## Findings

1. **F1 (blocking, fixed) — wrong per-user data-root name.** The original
   diff used `~/.local/share/unitill/webkit`. This product's one canonical
   Linux per-user data root is `universal-till`
   (`internal/paths/paths.go`'s `appNix`; `packaging/linux/uninstall-unitill.sh`'s
   `DATA_DIR`) — `unitill` is reserved for *system* paths
   (`/opt/unitill`, `/var/lib/unitill`) and unit/desktop filenames
   elsewhere in this package, never the per-user XDG root. Concretely:
   the uninstaller only knows how to delete `~/.local/share/universal-till`,
   so the wrong root would let the cookie file — the only place the raw
   session token lives on disk, since the DB stores only its hash —
   silently survive an uninstall the user was told wiped shop data.
   **Fixed**: renamed to `universal-till` in the implementation, its doc
   comment, and both test expectations; re-verified TDD red→green and the
   full gate afterward.
2. **F2 (recommended, applied) — `gtk_init_check()` added before touching
   WebKit.** The original C helper called `webkit_web_context_get_default()`
   before GTK was known to be initialized; `webview_go`'s own engine only
   calls `gtk_init_check()` afterward, in its constructor. Every
   documented WebKitGTK usage pattern initializes GTK first. Added an
   idempotent `gtk_init_check(NULL, NULL)` guard at the top of the C
   helper (a no-op cost on the normal path, since the engine's own later
   call becomes a no-op once GTK is up) that also makes a genuinely
   display-less launch fail the same way `webview.New()` is about to fail
   anyway, rather than constructing an unused context. Added
   `gtk+-3.0` alongside `webkit2gtk-4.1` on the file's own `#cgo pkg-config:`
   line to match.
3. **F3 (process gate, disclosed rather than silently skipped) — no real
   GTK display anywhere in this pipeline.** Neither this sandbox nor CI's
   `desktop-shell` job (build+vet only, explicitly "no tests and no
   window") can exercise the one thing this change actually does: write
   and re-read a cookie through WebKitGTK across a real restart. This
   mirrors this package's own standing, already-documented gap
   (`autostart_linux_test.go`: *"NOT run by CI ... verified manually
   before merge"*) for every other `desktop && linux` file — not a new
   shortcut introduced here. Unlike that precedent, this pipeline has no
   path to a real Linux till to do that manual verification itself
   (cold cloud cycle, no physical hardware). **Disclosed explicitly rather
   than claimed as done**: the pure path-resolution logic is genuinely
   CI-tested (TDD-verified), the cgo compiles and vets for real on CI, and
   the C call sequence has been checked line-by-line against the
   documented API — but the end-to-end "does the cookie actually survive a
   real restart" claim is unverified pending a real-hardware check. Flagged
   in the close-out for the product owner (who does have physical Pi5
   kiosks) rather than asserted as proven.
4. **F4 (design call, examined and agreed) — persists the whole jar, not
   just `ut_lang`.** This includes the login session cookie. Deliberate,
   for reasons independent of "matches macOS": both cookies are issued
   with explicit, non-zero `MaxAge` by server-side design — the in-memory
   jar was silently discarding what the server itself asked the client to
   keep, not enforcing a security boundary the product chose. There's no
   clean way to filter by cookie name in the 4.1 cookie-manager API short
   of hooking the `changed` signal and deleting entries after the fact —
   racier and more failure-prone than the thing it would guard.
   `webkit_darwin.go`'s `WKWebViewConfiguration` already gets a
   disk-backed `defaultDataStore` with no override, so macOS has shipped
   exactly this restart-surviving-session behavior already. Server-side
   revocation (`POST /api/auth/logout`) and `auth.SessionTTL` expiry are
   both unaffected by jar persistence either way.
   **Worth recording plainly (not a defect):** on Linux tills, power-
   cycling the shell no longer returns the operator to the lock screen
   within the session's 12h TTL. Checked `web/help/` for any promise that
   restart forces re-authentication — found none; the manual documents
   language as "a per-browser choice" and the explicit Lock/Exit-to-OS
   affordance as the deliberate way to end a session, not restart. No help
   topic needed updating. Worth one question to the product owner about
   whether any pilot shop treats restart-as-lock as an end-of-shift habit.
5. **F5 (checked, no action needed) — two Linux kiosk topologies exist.**
   `packaging/linux/unitill-kiosk-launch.sh` (cage + Chromium `--kiosk`) is
   a second, separate kiosk path from `cmd/unitill-desktop` (GTK/WebKit).
   Verified Chromium there uses no `--user-data-dir`/`--incognito`, so it
   already persists cookies via its default profile — this fix targets
   the genuinely broken path, and the reporter's issue body already names
   `cmd/unitill-desktop`/`unitill-desktop restart` specifically, so no
   ambiguity about which topology was affected.
6. **F6 (optional, applied) — extended `guard-webkit-version.sh`'s
   scope.** The guard's 4.0-regression grep didn't cover
   `cmd/unitill-desktop`, and nothing checked that this file's own
   `webkit2gtk-4.1` pin actually exists (as opposed to having regressed to
   4.0 or been silently removed). Added `cmd/unitill-desktop` to the grep
   path list and a second explicit existence check for
   `webkit_linux.go`'s own pin, mirroring the existing check on
   `internal/thirdparty/webview_go/webview.go`. Re-ran the guard's own
   regression test (`guard-webkit-version_test.sh`) — still passes.
7. **F7 (optional, applied) — README.** Added a "Linux WebKit cookie
   persistence (ut-docs#1233)" section to `cmd/unitill-desktop/README.md`,
   matching the existing per-topic section style (`## Linux startup gate`,
   `## Attach-vs-spawn cold-boot race`) — documents the new on-disk path
   and why only Linux needed the change.

## Manual verification beyond automated tests

None possible this cycle — no GTK display anywhere in this pipeline (see
F3). This is a headless/backend-only change (no template, no rendered
page, nothing to screenshot); the "look at it" verification standard this
skill otherwise applies to visible surfaces doesn't apply here in the
usual sense. What stands in its place: line-by-line C/API review against
the documented WebKitGTK 4.1 surface, full TDD red→green + mutation
verification on the pure logic, and CI's `desktop-shell` job compiling and
vetting the cgo for real. The one thing genuinely unverified end-to-end is
called out plainly in F3 rather than implied by a green pipeline.

No real client/shop name or secret-shaped literal in the diff. No local
server/process was started for this review (nothing to drive — headless
change).
