# Review: printerless Pi kiosk, no useless browser print dialog (ut-docs#6154)

**Change.** On a Pi kiosk till (cage + Chromium, installed by `packaging/linux/unitill-kiosk-setup.sh`, no CUPS), the browser print fallback opened a dialog whose only destination was "Save as PDF", which is useless under the kiosk cage. The till now detects that case and says what to do instead of opening the dialog.

- **Decision (BA):** detect, don't ship CUPS. CUPS alone would still leave the dialog empty, because nothing under cage can add a queue. It would also add a privileged daemon (with `cups-browsed`'s CVE history) to the first-boot script. Opt-in CUPS plus a printer setup UI is follow-up F1 (needs an ADR).
- **`internal/print/sysprobe.go`:**
  - `LpstatProber` runs `/usr/bin/lpstat -e` (absolute path, no shell, 2 s timeout, `WaitDelay`). A missing `lpstat`, or exit 0 with empty output, means **none**. A queue name means **present**. An error, non-zero exit or timeout means **unknown**, which fails open.
  - `CachedProber` uses a 60 s TTL. It never blocks a request: it refreshes in the background, dedups refreshes and wraps them in `RecoverAndLog`.
  - It is wired in `pages.Init` only when `isPiKioskAppliance`, with a boot warm-up.
- **`internal/pages/browser_print.go`:** `browserPrintOff(d, r)` is true only when all three hold: the till is a Pi kiosk, the request comes from loopback (IPv4, IPv6, v4-mapped), and the probe says **none**. Desktops, LAN browsers, Android and iOS are unchanged.
- **`internal/httpx/browser_print.go`:** a `browserprintoff` template function, bound per request in `withHelpHref`. `WithBrowserPrint` also binds `allowed` for the post-sale receipt render path, which bypasses `withHelpHref` (UX finding).
- **`web/ui/layouts/base.html`:**
  - `<html data-browser-print="off">` when the flag is on.
  - An always-rendered, hidden, reusable `#ut-print-unavailable` notice (`role="status"`, non-modal, never takes focus).
- **`web/public/inline-actions.js` `printPage()`:** the Android bridge still goes first. With the marker set, it shows the notice and never calls `window.print()`.
- **`web/public/app.js`:** toast dismiss hides a reusable notice instead of removing it. Auto-dismiss skips hidden and reusable notices.
- **Receipt:** "Retry print" becomes **Set up a receipt printer**, a Settings link shown only with the `settings` permission. A cashier sees "Ask a manager to set up a receipt printer." The render-failure string in `pos_api.go` follows the same rule.
- **Voucher page:** no slip form, and a "no printer" hint. The print-slip POST answers 409 inline: no slip, no false `via:"browser"` audit row.
- **Self-order customer pages:** they don't use `base.html`, so they get no marker, notice or link. A render test proves it.
- **i18n:** five new `en.json` keys with metadata. Language packs follow via Weblate (ADR-0160).
- **Help:** `web/help/en/printing.md` and `vouchers.md` are updated. `make docs-shots` changed only `manifest.json`.
- **No ADR:** this reuses the per-request template-flag pattern (`windowtoggle`), the existing Pi-appliance check and the existing loopback check. It adds no daemon and no network path.

**Pipeline.** BA, Architect (Opus 5.5), UX, Dev (Sonnet), Tester (Sonnet, separate agent), fix Dev (Sonnet), Reviewer (Opus 5.5, separate agent in a detached worktree).

## Findings

| # | Sev | Source | Finding | Outcome |
|---|---|---|---|---|
| U1 | major | UX | Receipt Print and the ask prompt's Paper fall back to `printPage()` on any failure, so "This till has no printer" would be false on a Pi whose USB printer is just unplugged. | **Fixed before build.** Key `receipt.printer.nothing_printed`: "Nothing was printed. …" |
| U2 | major | UX | `/settings` is a 403 dead end for a cashier. | **Fixed.** The link shows only with `allowed "settings"`; otherwise `receipt.printer.ask_manager`. |
| U3 | major | UX | The post-sale receipt render path bypasses `withHelpHref`, so `allowed` was false for everyone there. | **Fixed.** `WithBrowserPrint` binds `allowed` per request. |
| T1 | blocker | Tester | `scheduleToastDismiss()` removed the hidden notice 2.5 s after every load or swap. After that, Print on a printerless kiosk did nothing at all. The first e2e missed it because every click came within about 1 s. | **Fixed.** Auto-dismiss skips `[data-reusable]` and hidden notices. Two new e2e cases wait 3.5 s (after load, after an htmx swap); both were red before the fix and are green now. |
| T2 | minor | Tester | At 360 px the manager notice's text squeezed into a column about 38 px wide (notice 451 px tall). | **Fixed.** `#ut-print-unavailable .notice-text { flex: 1 1 14rem }`. A 360 px e2e asserts text ≥ 200 px wide and notice < 150 px tall; red before, green after. |
| T3 | nit | Tester | In `vouchers.md`, the kiosk sentence came before "The page then shows…". | **Fixed** (moved after). |
| R1 | minor | Reviewer | The three `receipt.printer.*` keys use a `review_exception` in the legal `receipt` namespace. | **Accepted.** They sit beside the existing `receipt.printer.retry` / `unavailable` keys, and the recorded rationale is accurate: operational chrome pointing staff at Settings, with no legal, fiscal or customer-facing receipt wording. Moving three keys to a new namespace would need an `areas.json` mapping for no reader benefit. |
| R2 | minor | Reviewer | "Check that the receipt printer…" is wrong when an invoice print fails on a Pi whose ESC/POS printer works. | **Fixed.** The text now reads "Check that the printer is set up and switched on." in en.json, help, the Go test and the e2e. |
| R3 | nit | Reviewer | A panicking probe left `busy` set forever, so no refresh would start again. | **Fixed.** `refresh` resets `busy` in a defer. New `TestCachedProber_PanicDoesNotWedge`: red with the old `sysprobe.go` (`State() = 0, want DestNone … calls=1`), green with the fix. |
| R4 | nit | Reviewer | With `lpstat` present but cupsd stopped, the probe returns unknown and the dialog stays. | Accepted: fail-open by design. The image ships no lpstat (F1). |
| R5 | nit | Reviewer | A future local reverse proxy would make LAN browsers look like loopback. | Accepted: no such proxy exists. A future proxy would have to pass the client address, which every other loopback check here already relies on. |
| R6 | nit | Reviewer | Auto-dismiss now skips every hidden notice, not only the reusable one. | Accepted: a hidden notice is not on screen, so there is nothing to dismiss. |

## Verification

- **TDD re-verified by an agent other than the author:** the Tester broke the production code six ways and saw each relevant test fail: the predicate (unknown treated as off; loopback check removed), the voucher 409 (branch removed; status 200) and the receipt link gating (link always shown; per-request `allowed` dropped). The Reviewer separately reverted three fixes (lpstat empty output gives none; `WithBrowserPrint` binds `allowed`; the voucher 409 writes no audit row) and saw red, then green.
- **Driven run (Tester):** a real till started with `UT_KIOSK=1` and no `lpstat` on the box, so the **server** set `data-browser-print="off"`. Checked at 1024×600, and at 360 px in `en` and `fa`:
  - Manager sees the link; cashier sees "Ask a manager…".
  - The voucher page has no button, and a direct POST gets 409 with no slip.
  - `window.print` is called 0 times, no dialog opens, and focus stays on Print.
  - The status bar, Lock and exit stay reachable (☰ menu at 360 px).
  - Touch targets: 228×51, 44×44 and 44 tall.
  - Self-order shows no marker or link.
- **Gate, run after the last edit:**
  - `gofmt` is empty; `go build ./...` and `go vet ./internal/...` are clean; `golangci-lint run ./...` reports 0 issues.
  - `go test -race` passes for `internal/print` and `internal/httpx`. Targeted `internal/pages` tests pass; the full non-race `internal/pages` run (Reviewer) passed in 338 s.
  - 61 of 62 `build`-job guards pass. `guard-shellcheck-version.sh` cannot run because this sandbox has no shellcheck binary; no shell script changed.
  - Playwright: the new spec plus the voucher-slip and invoice print specs gave 15/15 passed after the last edit. Earlier, the full set of existing print specs passed (one load flake, `settings-voucher-slip-4247`, passed twice when run alone).
- **Pre-existing, not this change:** `go test ./internal/plugins` exceeds go test's default 600 s timeout on unmodified `origin/main` too (the whole package takes 771 s). `TestHostTCPOpenDeniedWithoutPermission` alone passes in about 2 s.
- **Not verified:** a real Pi with a real touchscreen (UX gate G13), touch scrolling (headless drag doesn't scroll even without the notice), dark theme, and German strings.

Pre-flight: help topic updated (`printing.md`, `vouchers.md`) + `make docs-shots` run (manifest only); `en.json` keys → `en.meta.json` updated; `reference/` n/a (no reference page describes the browser print fallback); `README.md` n/a (no stale text); ADR n/a (no new decision); follow-ups F1–F4 → Backlog cards filed at close-out; language packs n/a (Weblate, ADR-0160).

**Verdict:** approve. Every blocker, major and minor is fixed or accepted with a reason.

**Deferred:**
- F1: opt-in CUPS plus a printer setup UI on the kiosk image (ADR first).
- F2: warn when "Regular printer" is chosen on a device with no `lp`.
- F3: check that the iOS and desktop shells really open a print dialog.
- F4: a PDF route off a printerless kiosk, only if asked.
- Dark-theme colours of the receipt printer warning (predates this change).
- Real-Pi device check.
