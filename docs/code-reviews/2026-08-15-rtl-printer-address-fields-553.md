# Code review: printer/kitchen-printer address fields corrupt under RTL locales

**Card:** universaltill/ut-docs#553
**Date:** 2026-08-15
**Complexity:** easy — built inline (session model), reviewed by an
independent fresh-context Sonnet subagent. One review round; no
blocker-class findings, so no second round earned.

## What shipped

`web/ui/pages/settings.html`'s printer card has three inputs that hold a
technical, non-localized value — a `host:port` network address, a device
path, and the kitchen-printer's own `host:port` — with no `dir="ltr"`. Under
an RTL locale (`fa`/`ar`), the page's inherited `dir="rtl"` reversed these
values inside their fixed-width boxes: right-aligned and visually
truncated/corrupted, exactly the same bug class independently found and
fixed on `/kitchen-stations` in ut-docs#516 (see
`docs/code-reviews/2026-08-12-kitchen-station-routing-516.md`), which
explicitly filed these three pre-existing fields as this card's own
follow-up.

- `web/ui/pages/settings.html:746,747,751` — added `dir="ltr"` to the
  `name="address"`, `name="device"`, `name="kitchenAddr"` inputs.
- `internal/pages/settings_page_test.go` —
  `TestSettingsPage_PrinterAddressFieldsAreLTR`, a new regression test
  asserting all three inputs render with `dir="ltr"`; confirmed (both
  independently by Dev and again by Review) to FAIL against the pre-fix
  markup and PASS after the fix.
- Repo-wide sweep for other missed technical-address fields (per the
  card's acceptance criteria): `grep -rn 'name="address"\|placeholder="192\.\|placeholder="/dev/\|placeholder="COM'
  web/ui/` plus a broader field-name sweep (`ip|host|port|serial|mac`) — no
  other field is missing the treatment. Confirmed independently twice (Dev,
  then Review).

## Independent review (Sonnet, fresh context)

Read the full diff and surrounding code, re-ran the sweep independently,
verified `dir="ltr"` matches this repo's own pre-existing convention (also
present on `country_settings.html`'s currency-code/tax-rate/retention-days/
currency-symbol inputs — not a pattern invented for this diff), reverted
just the HTML change to confirm the new test genuinely fails pre-fix and
passes post-fix, and ran the full verification suite independently.

**No blockers.** Two non-blocking notes, neither requiring a diff change:

- The new test's assertions match full attribute-order substrings (e.g.
  `name="address" value="" placeholder="192.168.1.50:9100" dir="ltr"`) —
  slightly more brittle than this file's shorter-substring convention
  elsewhere. Acceptable: it fails loudly (an obvious future test fix), not
  silently, and matches this file's existing literal-substring style in
  other places.
- The `address` field is dual-purpose: network mode holds `host:port`
  (the LTR technical string this fix targets), but system-printer mode
  holds an arbitrary printer name that could itself be RTL script. This is
  inherent to the field being shared across modes, not introduced by this
  diff, and mirrors the identical situation already accepted in the #516
  precedent. Not fixed here; flagged as a latent edge case only.

## Verified beyond the automated suite

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/pages/... -race -count=1 -timeout=900s` — green
  (`internal/pages` 605-609s under `-race`, right at the known ut-docs#648
  timeout-margin issue — pre-existing and unrelated to this diff, confirmed
  by running the same package with a longer timeout both before and after
  this change; not fixed here, out of this card's scope).
- Full repo gate: `go test ./... -race -count=1` — every other package
  green in the same run.
- `bash scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-help-topics.sh` — all
  green (this diff adds no SQL and no new user-facing strings, so the
  i18n/data-access guards are unaffected by it but confirmed green on the
  current tree regardless).
- No user manual update: this is a rendering-correctness fix, not a
  behavior/steps change — a shop owner already does the same thing
  (`web/help/en/printing.md`'s existing steps are unchanged); it now just
  renders correctly under `fa`/`ar`.
- No real client/shop name, no secret-shaped literal anywhere in the diff.

## Safe-to-merge verdict

Yes. Fix is complete and independently sweep-confirmed, matches established
repo convention, the regression test is genuine, and the full gate is
green.
