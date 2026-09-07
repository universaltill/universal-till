# Code review: keep the window-control status note live (ut-docs#1060)

**Branch:** `fix/1060-window-mode-status-live-poll` (2 commits: `71079fe`
fix, `c84d085` docs screenshot regen)
**Reviewer:** independent subagent, fresh context, no prior involvement in
the design or implementation.

## What

Settings › Display's window-control status note (which of three
mutually-exclusive truths applies: no desktop shell attached, a desktop
shell holding a live control poll, or the Pi kiosk appliance) used to be
computed once at `/settings` page-render time and then go stale if the
topology changed (a desktop shell attaching/detaching) while Settings
stayed open. Fix:

1. Extracted the note's markup into `web/ui/partials/window_mode_status.html`,
   following the existing `tse_provisioning_block.html`/
   `backup_restore_staged.html` pattern (included by filename in the full
   page render, and served standalone for its own htmx self-poll).
2. New `GET /ui/settings/window-mode-status` in `internal/pages/settings_page.go`
   rendering that same partial standalone.
3. The partial's root `<div>` self-arms `hx-get=".../window-mode-status"
   hx-trigger="every 15s" hx-target="this" hx-swap="outerHTML"` — omitted
   when `piKioskAppliance` is true, since that topology can't change
   post-boot.
4. Extracted `windowControlTopology(d *common.Deps) (shellAttached,
   piKioskAppliance bool)`, used by both the main `/settings` handler and
   the new endpoint, replacing two independently-maintained copies of the
   same boolean pair.
5. Registered the new partial in `internal/httpx/httpx.go`'s shared
   `renderFiles` list, needed for `{{ template "window_mode_status.html" . }}`
   to resolve inside the full-page render.
6. New test `TestWindowModeStatusFragment_LivePollReflectsCurrentTopology`.

## Independent verification

**Diff matches the description, no scope creep.** `git diff main..HEAD --stat`
is exactly the 5 code/test files claimed plus the docs commit's screenshot
regen (`web/help/img/manifest.json` + 3 unrelated PNGs — see note below).
No money, SQL, or plugin-verification code anywhere in the diff (grepped for
`money.Money`, `SELECT/INSERT/UPDATE/DELETE`, `manifest_verifier`,
`Ed25519` — zero hits), consistent with this being a pure read-only
UI/template change.

**Auth gating — correct, matches sibling `/ui/...` fragment endpoints.**
`internal/auth/middleware.go`'s `exempt()` allowlist (the only way a path
skips the session cookie check) does **not** list
`/ui/settings/window-mode-status` — it falls through to the same
session-required tier as `/settings` itself and as
`GET /ui/tills/pending-pairings` (`pending_pairings.go`), neither of which
is exempt either. No PIN/manager `elevationCheck` gate on either the new
endpoint or the main `/settings` GET handler (elevation there gates the
POST/save actions only) — the fragment exposes exactly the same read-only
fact the full page already shows to any signed-in user, so there's no new
information leak. `/self-order` and `/api/self-order/*` auth-exempt kiosk
routes are untouched by this diff; `guard-kiosk-engine.sh` passes.

**`GET /api/window-mode` (`internal/pages/window_state_api.go`) is
untouched**, confirmed directly: `git diff main..HEAD -- internal/pages/window_state_api.go`
is empty. It also doesn't independently duplicate `windowControlTopology`'s
logic (grepped for `shellAttached`/`piKioskAppliance`/
`KioskSystemdWindowController` in that file — only a comment mentions the
kiosk controller type), so there wasn't a third copy left unconsolidated.

**`windowControlTopology` nil-safety — bit-for-bit identical to the
original inline expression.** Diff shows the exact same
`d.Shell != nil && d.Shell.Attached(common.ShellAttachedWindow)` and
`isPiKioskAppliance(d.WindowCtl)` moved verbatim into the new function
(`settings_page.go:33-35`), called identically from both the `/settings`
handler and the new endpoint (`settings_page.go:46`, `:520`).
`isPiKioskAppliance` does a type assertion on a possibly-nil interface,
which is safe in Go (`ok` is simply `false`). Several existing tests
(`backoffice_mode_test.go`, `demo_seed_opt_in_test.go`, `fiscal_gate_test.go`,
`help_hint_test.go`, `osk_mode_test.go`, `self_order_mode_test.go`,
`ui_smoke_test.go`) register `registerSettings` with a bare/zero-value
`common.Deps` (no `Shell` set) and hit `/settings`, so the nil-Shell path is
exercised by the full suite, not just claimed in a comment — confirmed
green (see Verification below).

**htmx `hx-trigger="every 15s"` correctly stops for the Pi-kiosk-appliance
case, no templating bug found.** `web/ui/partials/window_mode_status.html:20-21`:
```gotemplate
<div id="window-mode-status"
     {{ if not .piKioskAppliance }}hx-get="/ui/settings/window-mode-status" hx-trigger="every 15s" hx-target="this" hx-swap="outerHTML"{{ end }}>
```
When `piKioskAppliance` is true this renders `<div id="window-mode-status" >`
(a harmless extra space, no dangling attribute) with no `hx-get`, so htmx
never arms a poll — matches the established pattern in
`order_tracking_status.html` (`{{ if .Poll }}hx-get=...{{ end }}`) and
`pairing_wait.html` exactly. `{{ if }}/{{ end }}` pairs balance; no stray
literal `{{ }}` inside an HTML comment (checked both files' `<!-- -->`
blocks by eye — the surrounding doc comments use real `—`/`ADR-0064`
prose, no accidental template syntax). `go build`/`go vet`/`go test` all
parse and execute both the full-page and standalone-fragment template
paths without error, which would have failed loudly on any unbalanced
`{{ }}`. `id="window-mode-status"` appears exactly once in `web/ui/**`
(grepped) — no duplicate-ID collision with the enclosing `settings.html`.
The fragment sits inside `<form id="window-mode-form">` — a block-level
`<div>` inside a `<form>` is valid HTML and the poll (`hx-get` on the div)
is independent of the form's own `hx-post`, so no interference.

**i18n — zero new keys, confirmed, not just claimed.**
`git diff main..HEAD -- web/locales/` is empty. `bash scripts/ci/guard-i18n.sh`:
```
✓ i18n guard: 1456 template keys resolve; all locales match en.json; no hardcoded
Go-side response strings found; no hand-written hx-vals literals found; no
hardcoded inline-JS status strings found; no hardcoded ToastMessage literals
found; no missing Go-side i18n key literals found
```
The three `{{ T "settings.display.window_mode_*" }}` calls in the new
partial are the exact same keys that existed inline in `settings.html`
before — moved, not added.

## Full gate — run personally, exact output

Ran in a detached-HEAD `git worktree` checked out at
`fix/1060-window-mode-status-live-poll` (this worktree was already
occupied by the `worktree-agent-*` branch at `main`, so the branch itself
couldn't be checked out a second time — a detached worktree at the same
commit is equivalent for verification purposes).

- `gofmt -l .` — no output (clean).
- `go build ./...` — clean, no output.
- `go vet ./...` — clean, no output.
- `go test ./...` (full suite, 60+ packages) — all `ok`, including
  `internal/pages` (157.7s, contains the new test),
  `internal/httpx` (0.517s, the `renderFiles` change), `internal/auth`
  (2.671s, the gating middleware). No failures anywhere in the tree.
- `golangci-lint run ./...` — `0 issues`.
- `bash scripts/ci/guard-data-access.sh` — `✓ data-access guard: no inline
  SQL outside internal/data / internal/db`.
- `bash scripts/ci/guard-kiosk-engine.sh` — `✓ kiosk-engine guard: no
  self-order route handler references the cashier's Engine`.
- `bash scripts/ci/guard-page-http-error.sh` — `guard-page-http-error: OK`.
- `bash scripts/ci/guard-help-topics.sh` — `✓ help-topics guard: ... every
  page route has a claiming topic` (the new `/ui/settings/window-mode-status`
  route is correctly out of scope — `/ui/` is denylisted by
  `checkhelptopics/routecoverage.go:36` as "htmx fragments swapped INTO
  pages — the enclosing page's topic covers them").
- `bash scripts/ci/guard-docs-shots.sh` — `✓ docs-shots guard: 25 routed
  topics × 4 locales screenshotted and fresh (surface 9162d4ab2d48…)` —
  matches the regenerated `manifest.json` hash in the docs commit.
- `bash scripts/ci/guard-i18n.sh` — see i18n section above, passes.
- `bash scripts/ci/guard-compliance-claims.sh` — `✓ compliance-claims
  guard: 246 file(s) scanned, no forbidden fiscal-compliance claims found`.
- `bash scripts/ci/guard-htmx-loaded.sh` — `✓ htmx guard: 3 standalone
  template(s) using hx-* all load htmx.min.js`.
- `bash scripts/ci/guard-webkit-version.sh` (unrelated area, spot-checked
  for completeness) — passes, unaffected.

Every CI-blocking guard this change could plausibly touch was run and
passes.

## TDD claim — re-verified personally, not taken on trust

Reverted only `internal/httpx/httpx.go`, `internal/pages/settings_page.go`,
`web/ui/pages/settings.html`, and deleted `web/ui/partials/window_mode_status.html`
(kept `settings_page_test.go` at its post-fix state) via `git apply -R` on
an isolated diff of just those four files:

```
=== RUN   TestWindowModeStatusFragment_LivePollReflectsCurrentTopology
    settings_page_test.go:2411: GET /ui/settings/window-mode-status = 404
--- FAIL: TestWindowModeStatusFragment_LivePollReflectsCurrentTopology (0.07s)
FAIL
```

Genuinely fails with a 404, exactly as the route not existing would produce
— not a false-positive test. Restored the four files
(`git checkout -- ...` / `git checkout HEAD -- ...`), re-ran:

```
--- PASS: TestSettingsDisplayWindowControlNoteTellsTheTruthInAllThreeCases (0.24s)
--- PASS: TestWindowModeStatusFragment_LivePollReflectsCurrentTopology (0.18s)
PASS
```

Both pass post-restore. TDD claim confirmed genuine.

## Race conditions, resource leaks, edge cases

None found. The new handler is a plain stateless `GET` closure over `d`
(the same `*common.Deps` every other handler in `registerSettings`
closes over) — no goroutines started, no locks taken, no writes to shared
state, just two boolean reads and a template render. `windowControlTopology`
is a pure function with no side effects. The 15s client-side poll is the
same cadence and shape as the pre-existing `order_tracking_status.html`/
`pairing_wait.html` polls, so it introduces no new server-side load
pattern. `RenderPartial` (`internal/httpx/httpx.go:783`) parses only the
single named file (not the whole `renderFiles` list), matching the
existing `pending_pairings.html`/`tse_provisioning_block.html` usage — the
`renderFiles` registration (item 5) is needed only for the full-page
`Render()` path's `{{ template "window_mode_status.html" . }}` include,
which is correctly where it's used.

## Money / data-access / plugin-verification

Confirmed not applicable — no `internal/money.Money`, no raw SQL text, no
`internal/plugins` touches anywhere in the diff (grepped directly, see
above). `guard-data-access.sh` passes.

## Findings

**Blocking:** none.

**Should-fix:** none.

**Nice-to-have / observations, non-blocking:**

1. The docs commit (`c84d085`) regenerates 3 screenshots
   (`web/help/img/ar/sell.png`, `web/help/img/fa/multitill.png`,
   `web/help/img/fa/till-designer.png`) that have nothing to do with
   Settings › Display, plus `manifest.json`'s surface hash — but *not* any
   settings-topic screenshot. This is expected collateral of
   `make docs-shots`: the surface hash covers all of `web/ui/**` +
   non-test `internal/pages/**.go`, so any page-affecting code change
   forces a full re-render pass, and only images that come out
   byte-different (headless-browser font/anti-aliasing jitter, not a real
   content change) get committed. `guard-docs-shots.sh` passes and this is
   consistent with how the same guard behaves on other unrelated branches
   in this repo's history — not a defect, just worth naming so it isn't
   mistaken for evidence of a missed file.
2. `window_mode_status.html`'s root `<div>` has no `aria-live` region, so a
   screen reader won't announce the note changing while the poll updates
   it live — arguably the more relevant place for one now that the content
   genuinely can change under the user without an action on their part.
   Not a regression and not a deviation from convention: the closest
   analog, `order_tracking_status.html` (also a 15s self-polling status
   fragment), has no `aria-live` either, and the only `aria-live` in this
   whole family of fragments (`pairing_wait.html:32`) is on a distinct,
   normally-hidden message, not the polled status text itself. Flagging as
   a pre-existing gap across the pattern this change extends, not
   something this diff introduced or made worse.

## Verdict

**PASS.** The diff does exactly what the commit messages and task
description claim, with no scope creep. Auth gating matches the rest of
`/settings` and sibling `/ui/...` fragment endpoints — no exemption gap,
no elevation gap, no information leak (the fragment shows only what the
full page already shows any signed-in user). `GET /api/window-mode` is
genuinely untouched. `windowControlTopology`'s nil-safety is a verbatim
extraction of the original expression, exercised by existing bare-Deps
tests. The htmx trigger correctly self-disarms for the Pi-kiosk-appliance
case with no templating bug. Zero new i18n keys, locales still match.
Full gate (`gofmt`, `go build`, `go vet`, `go test ./...`, `golangci-lint`,
and every plausibly-relevant CI guard) passes with real, reproduced output.
The TDD claim was independently re-verified: the new test fails with a 404
against the pre-fix code and passes against the fix. No race conditions,
leaks, money/SQL/plugin concerns. Two non-blocking observations recorded
above, neither warranting a hold.
