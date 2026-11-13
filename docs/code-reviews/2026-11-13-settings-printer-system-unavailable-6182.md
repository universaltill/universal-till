# Review: Settings → Printer warns when the till has no system printer (ut-docs#6182)

**Change.** On a Pi kiosk till whose own device has no OS print destination, choosing **Regular printer (system)** under Settings → Receipt printer → Connection saved fine but could never print, because `lp` had nowhere to send the receipt. Settings now shows an inline, advisory warning under the Connection select whenever that mode is picked on such a till.

- **`internal/pages/browser_print.go`:** `systemPrinterMissing(d)` is true only when `d.SystemPrinters` is non-nil and `State() == print.DestNone`.
  - It does not depend on the viewer and has no loopback check, unlike `browserPrintOff`, because `lp` runs on the server.
  - Only the Pi kiosk wires the probe (`init.go`, #6154). Every other till has a nil probe, so the function returns false.
  - The render path reads only `CachedProber.State()`. That call is non-blocking: when the value is stale it starts one background refresh. The render path never calls `Refresh` or exec.
- **`internal/pages/settings_page.go`:** passes the new template flag `printerSystemUnavailable`.
- **`web/ui/pages/settings.html`:**
  - `<p class="notice-block-warn" id="printer-system-unavailable" role="status">` is rendered only when the flag is true. It is `hidden` unless the saved mode is `system`, and it ends with `{{ helpLink "printing" }}`.
  - The `mode` select gets `aria-describedby` while the warning is shown.
  - A delegated `change` listener on `#settings-printer`, which sits outside `#settings-printer-region`, toggles `hidden` and `aria-describedby` live, before Save. It does nothing when the element is absent.
- **i18n:** a new key, `settings.printer.system_unavailable`, with `en.meta.json` metadata (`review: standard`). Language packs follow via Weblate (ADR-0160).
- **Help:** `web/help/en/printing.md` gains a sentence in setup step 1 and in "Nothing is printing" item 4. `web/help/img/manifest.json` has a refreshed `surface_sha256`.
- **Tests:** a Go predicate table, a server-render table (probe none/unknown/present/nil × mode), an AC5 check that saving with `system` still returns 204, and a Playwright spec covering the live toggle and the region swap.
- **No ADR:** this reuses the #6154 probe and the per-page template-flag pattern. It adds no new decision, daemon or network path.

**Pipeline.** BA, Architect, UX, Dev (Sonnet), Tester (separate agent), Reviewer (Opus 5.5, separate agent in a detached worktree at `c6a87b8c2`).

## Findings

| # | Sev | Source | Finding | Outcome |
|---|---|---|---|---|
| R1 | nit | Reviewer | `settings_printer_system_unavailable_6182_test.go:86`: the negative branch ("no element id when the probe is not none") can never fire. The always-rendered card script contains `getElementById('printer-system-unavailable')`, so the `!Contains(...)` guard is always false. Coverage is still real, because the `data-testid` regex at :76 and the `aria-describedby` check carry the negative cases (verified: reverting the template turns the positive cases red). | Fixed by the orchestrator: the branch now asserts `id="printer-system-unavailable"` is absent; 6182 tests re-run green. |
| R2 | nit | Reviewer | `settings.html:1924`: `role="status"` on a pre-rendered `hidden` element. Some screen readers do not announce a live region on un-hide. Focus stays on the select, and `aria-describedby` is set at the same moment, so the description may not be re-read until the next focus. | Accepted: the same pattern as #6154's `#ut-print-unavailable`. The warning is advisory and visible, and the Tester attested `role` and `aria-describedby` (UX gate 5). |
| R3 | nit | Reviewer | The warning reflects the cached probe at render time (60 s TTL). Within the boot warm-up window the state is `DestUnknown`, so nothing is shown. A queue added or removed after the page loads shows only after a reload or a Save swap. | Accepted: fail-open by design (#6154 R4), and the warning is advisory only. |
| R4 | nit | Reviewer | The card now has two `?` links to `printing`: the heading's and the warning's. | Accepted: UX A3 asked for the in-warning link. Both are 27×27 (Tester gate 8). |

No blocker, major or minor findings. Every item in the brief was checked:

- **Offline-first / render never blocks:** only `State()` is called. `Refresh` is reached only from the `init.go` warm-up goroutine.
- **XSS:** the copy goes through `T`, so it is HTML-escaped (`can&#39;t`). `helpLink` takes a constant id and returns `template.HTML` built by `helpLinkHTML`. No request data reaches the warning.
- **Inline script / CSP:** the new `<script>` block has the same shape as the card's existing ones (the #1556 discovery script, the #2902 test button). It is a plain inline IIFE with no nonce, the same as its neighbours, and contains no user-facing strings, so it needs no `var T`.
- **HTMX swap / double-binding:** the listener is bound once, on `#settings-printer`. `UT.refreshRegion` replaces only `#settings-printer-region`, so the card survives the swap and the listener is never re-bound. The script looks up the warning and reads the select per event. `#settings-printer` contains only one `select[name=mode]`.
- **`hidden` vs CSS:** `.notice-block-warn` sets no `display`, so the UA `[hidden]` rule applies.
- **RTL:** no new CSS. `.row-warn-icon` uses `margin-inline-end`.
- **No `showModal`, no native confirm:** none added. Saving with `system` opens no dialog (Tester gate 9).
- **Recurring bugs:** no file writes and no filesystem paths in the diff, so `os.MkdirAll` and `paths.Data` do not apply.
- **Fixed dates:** none in the new tests.
- **Seed data:** only an RFC1918 address (`192.168.1.50`). No real shop names or secrets.

## Verification

- **TDD re-verified by the Reviewer, in the worktree:**
  - I restored `web/ui/pages/settings.html` from `origin/main` and ran `go test ./internal/pages/ -run 6182`. `TestSettingsPrinter_SystemUnavailableWarning_6182/{none_system,none_network,none_off}` failed with `warning present = false, want true`.
  - I put the HEAD version back, and the 6182 tests passed (`git status` clean).
- **Reviewer gate:**
  - `go build ./...` is clean, `go vet ./internal/pages/...` is clean and `gofmt -l internal/pages/` is empty.
  - `go test ./internal/pages/ -run '6182|BrowserPrint|6154' -count=1` passes: 16 tests, including all #6154 `BrowserPrint*` and `VoucherSlip_BrowserPrintOff_*` tests.
  - These guards pass: `guard-i18n`, `guard-i18n-meta`, `guard-rtl-logical-css`, `guard-no-showmodal`, `guard-no-native-confirm`, `guard-fixed-dates`, `guard-help-topics`, `guard-compliance-claims` and `guard-docs-shots` (surface `b802c65c50ea…` fresh).
- **Tester attestation (6182-test.md, verdict PASS):**
  - Every `run:` step of `ci.yml`'s `build` job passed: 162 PASS, 0 real FAIL. The two shellcheck steps failed only because shellcheck was missing from PATH, and passed when re-run with shellcheck 0.11.0.
  - `golangci-lint`: 0 issues. `go test ./...` is ok apart from the known pre-existing `internal/plugins` timeout.
  - Playwright `settings-printer-system-unavailable-6182.spec.ts`: 1 passed.
  - UX §7 gates 1–10 pass. They covered:
    - layout at kiosk 1024×600 and 360 px;
    - the live toggle surviving the Save/`refreshRegion` swap;
    - focus retention and no on-screen keyboard;
    - `de` wrapping and `fa` RTL in light and dark;
    - contrast of 15.71:1 (light) and 12.44:1 (dark);
    - the help link;
    - an advisory save returning 204 with no dialog;
    - no markup for the nil, Present and Unknown probes.
  - **Emulated, not verified on hardware:**
    - **Touch:** Chromium `hasTouch` plus raw CDP touch events, so gate 4 (touch pan) is emulated.
    - **German:** a long string was injected, so the `de` copy is simulated.
    - **Real Pi:** no real Pi kiosk with CUPS installed and zero printers (`lpstat -e` empty) was available, and no real touch hardware (gate 11 NOT VERIFIED). The `lpstat`-empty → `DestNone` path is covered by `internal/print` unit tests. The e2e till is not a Pi, so the spec injects the server's exact markup to drive the script, and server rendering is covered by the Go tests.

Pre-flight:
- Till user-visible behaviour → `web/help/en/printing.md`: updated.
- `make docs-shots`: updated (`manifest.json` surface hash only; `guard-docs-shots` fresh).
- `reference/` page: n/a (no ut-docs reference page describes printer Connection modes; grep found none).
- `README.md`: n/a (no stale text).
- New `en.json` key → `en.meta.json`: updated.
- Language packs: n/a (Weblate, ADR-0160).
- ADR: n/a (no new decision).
- Platform/plan claims: n/a.
- Follow-ups: see Deferred.
- Review record: this file.

**Verdict:** approve. There are no blockers, majors or minors. R1 (test cleanup) is fixed; R2–R4 are accepted with reasons.

**Deferred:**
- Real-hardware check on a Pi kiosk with CUPS installed and zero queues, plus real touchscreen panning (Tester gate 11). Fold this into the #6154 hardware follow-up rather than a new card, unless the Scrum Master prefers one.
- #6154 F1 (opt-in CUPS and printer setup on the kiosk image, ADR first) remains the real fix. This warning only steers the operator to Network or USB device.
