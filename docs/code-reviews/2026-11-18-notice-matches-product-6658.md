# Review — the shipped registration notice matches the product (ut-docs#6658)

Branch `fix/6658-notice-matches-product`. Tester + Reviewer pass run inline by
a Sonnet 5.5 agent, a different session from Dev.

## What shipped
The owner-approved notice (checkin-notice-v2) says: "in Settings > Privacy,
switch off "Send usage facts"" and lists the release channel. The product
now matches it, with the notice text untouched:
- New manager-only Settings > Privacy card (`#settings-privacy`, sidebar row
  `settings-privacy` filtered for cashiers like `subscription`). It holds the
  former "Cloud record" block (status, last check-in, notice) and the
  "Send usage facts" switch, a change-triggered checkbox: on = `stop=0`,
  off = `stop=1`, still shop-wide `registration.stopped`.
- `POST /api/settings/registration-stop` now refuses anything but an explicit
  `stop=0|1` (400), so a dropped `hx-vals` can never read as "resume".
- The check-in carries `device.channel` (`buildinfo.CurrentChannel()`: a
  build constant of sideload/play/appstore/direct, ADR-0194), set before
  `DeviceExtra`.
- "Stop sending"/"Resume sending" keys removed; registry, agent catalogue,
  status-bar link, help (`claim.md`, `users.md`), CHANGELOG, i18n meta and
  help-breadcrumb baseline (ar/fa/tr, Weblate follow-up) updated.

## Verified
- `git diff origin/main...HEAD -- web/locales/en.json` has no added or removed
  line on any `registration.notice.*` key: notice text unchanged.
- No `Stop sending`, `registration.stop`, `registration.resume` leftovers or
  dead keys in any locale (`git grep`); only `stop_help`, `resume_help` and
  `title` remain, and the template uses them.
- Gating: the card renders only for managers (`registrationCard` is nil
  otherwise), the sidebar row is hidden for cashiers, and the POST is still
  elevation-gated (a cashier gets the PIN prompt).
- The channel value is a fixed enum, not shop data: no PII. A stopped or
  unpaid-auto-registered till still sends only the allow-list.
- `go build ./...`, `go vet` (pages, cloudsync, uislot) and `gofmt -l` are
  clean; `go test` for pages, cloudsync, enroll, uislot, data and httpx pass.
- Guards pass: `guard-i18n`, `guard-i18n-meta`, `guard-data-access`,
  `guard-help-breadcrumbs`, `guard-help-terms`, `guard-help-topics`,
  `guard-help-drift` (warnings only), `guard-fixed-dates`.

## Findings
1. **medium, open (not fixable on this host)**: `guard-docs-shots.sh` fails.
   `web/ui/**` and `internal/pages/*.go` changed (surface hash), and
   `web/help/en/claim.md` and `users.md` changed (topic hashes). `make
   docs-shots` cannot run on macOS (font pin needs Inter, DejaVu and Noto in
   the CI container) and deleted PNGs when it aborted; those were restored
   with `git checkout`. The manifest must be regenerated in the Linux
   docs-shots environment (the Settings and claim screenshots probably show
   the moved card). Not faked with the surface-hash escape hatch, since
   pixels may change.
2. nit (accepted): ar/fa/tr `users.md` item 10 breadcrumbs still carry the old
   wizard text; baselined, Weblate re-translates (ADR-0160).
3. nit (accepted): the switch was not driven in a real browser here; handler
   and render are covered by `privacy_card_test.go` and
   `registration_card_test.go`.

## Process
Tester: no screenshots looked at (docs-shots could not run on macOS), so the
dark/light, RTL and 10-inch layouts of the new card were NOT visually
checked. Reviewer: Sonnet 5.5, inline; no revert/restore re-run of the new
tests beyond the passing suites.

Pre-flight: help topic `updated` (claim, users; English only, ADR-0160);
docs-shots `open` (finding 1); en.json/en.meta `updated` (English + meta
only); reference page `n/a`; ADR `n/a` (ADR-0200 implemented as written);
README `n/a`; review record — this file.
