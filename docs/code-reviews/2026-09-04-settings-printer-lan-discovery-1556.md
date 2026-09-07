# Code review: Settings → Printer LAN discovery (ut-docs#1556)

**Date:** 2026-09-04
**Author:** Farshid Mirza (pipeline), reviewed independently before merge
**Card:** universaltill/ut-docs#1556

## What shipped

The till's own receipt printer (Settings → Printer) had no LAN discovery,
even though the machinery already existed for Kitchen Stations (#140): a
bare address text box plus a Test button, while a shop owner had to type
the receipt printer's IP by hand.

This change adds a **"Find printers on this network"** button to the
Settings → Printer card, reusing the *exact same* endpoint Kitchen
Stations already calls — `GET /api/kitchen-stations/discover-printers`
(`internal/discovery.BrowsePrinters`, manager-gated, 4s bounded mDNS
scan) — from a new button on `web/ui/pages/settings.html`. No new Go
route or handler: this is a template + JS + i18n + docs change only.

The printer card has two address fields (receipt printer `address`,
kitchen printer `kitchenAddr`); one scan now serves both, via two
per-candidate buttons ("Use for receipt printer" / "Use for kitchen
printer") on each result row, mirroring `kitchen_stations.html`'s
existing click-to-scan JS pattern (plain `fetch()`, no htmx, a candidate
only fills a field on tap — never auto-submits or auto-wires anything).

Button and results area are wrapped in `{{ if .isManager }}`, matching
the existing precedent two lines below in the same card (Test print /
receipt-designer link) — the underlying endpoint 403s a cashier, so an
un-gated button would reproduce the exact "visible but silently blocked"
bug class ut-docs#866/#867 already fixed for those two controls.

The two honest limitations the card asked to state, not hide, are in the
new copy: only AppSocket/JetDirect (raw-socket) printers are found, not
IPP-only ones, and USB/internal printers are invisible to any LAN scan —
both the empty-state message and the help text name this explicitly,
replacing the bare "No printers found" the Kitchen Stations version has
(left as-is; fixing that page's copy is a natural follow-up, out of scope
here).

### Files touched
- `web/ui/pages/settings.html` — new button/results markup, new
  `id="printer-address-input"` / `id="printer-kitchen-addr-input"` on the
  two inputs, new inline `<script>` block.
- `web/locales/{en,ar,fa,tr}.json` — 8 new `settings.printer.discover.*`
  keys, genuinely translated (not copied English) in all 3 non-English
  locales.
- `web/help/{en,ar,fa,tr}/printing.md` — new step describing the button,
  its manager-only gating, and its AppSocket/JetDirect-only limitation.
- `web/help/img/**` + `manifest.json` — regenerated via `make
  docs-shots` (printer card's visible layout changed). `sell.png`/
  `invoices.png` picked up tiny non-deterministic pixel diffs in a few
  locales from the full re-run; unrelated to this diff (neither page nor
  its Go/template files were touched here).
- `internal/pages/settings_page_test.go` — extended
  `TestSettingsPage_PrinterCardHidesTestPrintAndDesignerLinkFromCashier`
  with assertions for the new button; added
  `TestSettingsPage_PrinterCardHasDiscoverableAddressFieldIDs`; fixed the
  pre-existing `TestSettingsPage_PrinterAddressFieldsAreLTR`'s exact-
  substring assertions, which broke against the new `id` attributes (not
  a behavior change — same fields, same `dir="ltr"`, just a different
  exact string to match).

## Independent review

Reviewed by a fresh-context Sonnet subagent (per this card's
`complexity:easy` label — "different model" relaxes to "different
instance" at this tier), isolated in its own git worktree, with explicit
instructions to run things rather than only read the diff.

**Verdict: SAFE TO MERGE. No blocking findings.**

What it actually ran and confirmed:
- `go build ./...`, `go vet ./...`, `gofmt -l .` — all clean.
- `go test ./internal/pages/... -run 'TestSettingsPage|TestDiscoverPrinters' -v`
  — all pass.
- `scripts/ci/guard-i18n.sh`, `scripts/ci/guard-help-topics.sh`,
  `scripts/ci/guard-docs-shots.sh`, `scripts/ci/guard-compliance-claims.sh`
  — all pass; the guard's independently-recomputed surface hash matched
  the committed `manifest.json` exactly, confirming the screenshot regen
  is genuine, not stale.
- **Independently re-verified both new/changed tests actually catch
  regressions** (mutate → confirm fail → restore → confirm pass, done
  entirely inside its own isolated worktree, never the shared checkout):
  removing the `{{ if .isManager }}` guard made the cashier-gating test
  fail as expected; removing `id="printer-address-input"` made the new
  field-id test fail as expected. Both restored cleanly afterward.
- Checked the two recurring bug classes this pipeline watches for
  (missing `os.MkdirAll` on a file-write handler; a cwd-relative path
  where `paths.Data(...)` belongs) — not applicable, no Go handler code
  in this diff at all.
- Checked the JS for XSS: candidate `name`/`address` values go through
  the existing `esc()` (`textContent` → `innerHTML` round-trip) helper
  before insertion — no raw interpolation of untrusted scan data.
- Checked RTL: no literal `left`/`right` introduced; the new JS uses the
  logical `paddingInlineStart` property, consistent with the rest of the
  file.
- Checked for real client/shop names or literal secrets in the diff —
  none found.

Minor non-blocking observation carried forward: the 8 new locale keys
will need a follow-up in `ut-plugin-language-{de,es}` per the
`lang-pack-drift` rule — advisory-only on this PR (doesn't touch `main`
directly), tracked as a natural follow-up rather than a merge blocker.

## Verified beyond automated tests

A real driven Playwright run against the live app (not just rendered-HTML
assertions), before the independent review:
- Manager session: button renders, click triggers the real scan against
  `GET /api/kitchen-stations/discover-printers`, and — with nothing to
  discover on this sandboxed network — the new empty-state copy renders
  correctly ("No AppSocket/JetDirect printers answered on this network.
  If yours is IPP-only or connected by USB, enter its address or device
  path above."). Zero browser console errors.
- With a mocked API response carrying two candidates (one named, one
  with an empty name to exercise the generic-name fallback): both
  per-candidate buttons render, and clicking "Use for kitchen printer" /
  "Use for receipt printer" fills the correct, independent field —
  screenshotted and visually reviewed (labels aligned to their own
  fields, no overlap/clipping, generic-name fallback reads correctly).
- Farsi (RTL) at a 1024×600 kiosk-ish viewport, with the same mocked
  candidate: layout mirrors correctly end-to-end (nav, form fields, the
  two-button candidate rows), no broken/overlapping text.
- **Not separately re-verified via a real browser**: the cashier-hides-
  button behavior. The e2e Playwright project runs under `UT_AUTH=off`
  with no real cashier session reachable, so this is covered only by the
  Go handler-level test (`TestSettingsPage_PrinterCardHidesTestPrintAndDesignerLinkFromCashier`),
  which the independent review separately confirmed actually fails when
  the guard is removed. Accepted as sufficient for an `easy`-tier,
  template-only change — noted explicitly rather than silently assumed
  covered.

## Safe-to-merge verdict

Yes. Build/vet/tests/guards all green, independent review found nothing
blocking, TDD claims re-verified personally by the reviewer in an
isolated worktree, manual updated in the same branch, no scope creep
beyond the card's stated acceptance criteria.

## Explicitly deferred (not this card)

- Kitchen Stations' own empty-state copy (`kitchenstations.discover.none_found`)
  stays a bare "No printers found on this network." — this card
  deliberately did not touch that page; worth a small follow-up card.
- IPP-only printer discovery (ut-docs#1527) and the unified "Devices"
  panel (ut-docs#1526) — both explicitly out of scope per the card body.
- The 8 new locale keys' follow-up in `ut-plugin-language-{de,es}` per
  `lang-pack-drift` (advisory on this PR, not blocking).
