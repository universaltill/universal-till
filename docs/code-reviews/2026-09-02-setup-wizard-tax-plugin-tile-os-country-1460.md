# Code review: setup wizard's German tax-plugin tile never appeared unless the OS-detected country was already Germany (ut-docs#1460)

## What shipped

`ut-docs#1460`, reported by the product owner live on the TECLAST pilot
tablet right after a clean rebuild: `"fiskaly german tax is not installed
automatically on the till and only german language did."` The wizard's
step 3 tax-plugin install tile (ut-docs#1180, ADR-0025 decision 4) never
appeared, so `com.universaltill.tax-de` was never offered — a German till
came out of setup with no fiscal plugin and no trace of why.

**Root cause.** `internal/pages/setup_page.go`'s `renderWizard` resolved
`installableTaxPlugin` against `code` — the *OS-detected* country
(`detectCountry`) on the wizard's very first `GET`, since no `POST` has
happened yet. But step 2's country picker is pure client-side Alpine
(`x-data`) with **no server round-trip** on a tile click — `step 2 → step 3`
is a bare `step = country === 'DE' ? 3 : 4`. So the operator's actual pick
never reaches the server before step 3 renders, and the FIRST render is the
only chance this tile's markup ever lands in the DOM at all; Alpine only
ever *shows/hides* whatever the server already sent. A till whose OS
locale/timezone isn't already German-tagged (the pilot tablet's
`en-GB`/`Europe/London`) got no tile in the DOM, full stop — no later
hand-picked "Germany" could resurrect markup that was never rendered.

**The fix.** `internal/pages/setup_page.go`: resolve `installableTaxPlugin`
against `tseProvisionCountry` (the existing `"DE"` constant `setup_tse.go`
already uses to gate the whole step 3 flow — ADR-0053), not the dynamic
`code`. Safe because the tile only ever renders inside `<section x-show="step
=== 3">`, and step 3 is Germany-only by design regardless of `code` — so
hardcoding the country here doesn't leak the tile to any other operator; it
sits in the DOM unconditionally now, and Alpine reveals it only to whoever
actually reaches step 3 (i.e., whoever picked Germany).

## Independent review (Opus, isolated worktree, revert-then-restore TDD re-verification)

Full findings below; summary first. Verdict: fix is correct, no path found
that leaks the tile to a non-DE operator (traced every route `step` can
become 3: step-2 Next, step-4 Back, the PIN-error `errStep`, and the
`?tax_country=` resume — all four are independently DE-gated). TDD claim
re-verified by hand: reverted the one-line fix, confirmed both new/updated
tests fail with real assertion messages, restored, confirmed green again.
`gofmt`/`go build`/`go vet`/`go test ./internal/pages/...` and the CI
guards (`guard-data-access`, `guard-kiosk-engine`, `guard-page-http-error`,
`guard-i18n`, `guard-compliance-claims`, `guard-help-topics`,
`guard-docs-shots`, `guard-makefile-version`, `check-brand-assets`) all
independently re-run and clean. No SQL/money/i18n/file-write/`paths.Data`
concerns (confirmed N/A for this diff). No real client name, no
secret-shaped literal.

Findings, and what was done with each:

- **F1 (fixed).** The safety guarantee moved from a tested Go conditional
  to an untested Alpine ternary in `setup.html`, and nothing in the Go
  suite pinned it — a later change making step 3 reachable for any other
  reason (a step-jump nav, a "confirm business details" step for every
  country) could ship the German tile to a French operator with a fully
  green suite. Fixed: `TestSetupWizardShowsTaxPluginInstallTileForDEOnly`'s
  FR half now asserts both `step = country === 'DE' ? 3 : 4` (step 2's
  Next) and `step = country === 'DE' ? 3 : 2` (step 4's Back) are present
  in the rendered body, in addition to the tile markup itself.
- **F2 (documented, not code — accepted).** `setupInstallableTaxPlugin`
  used to short-circuit on the `countryTaxLocale` miss *before* ever
  calling `setupTaxCatalogEntries`, so a non-DE till never issued the
  tax-catalog fetch. Post-fix it always does — a second bounded (3s
  timeout), 5-minute-TTL-cached fetch on every first-boot render,
  worldwide, not just for German tills. Not an offline-first violation
  (bounded, cached, never on the checkout path) but a real small
  first-boot-latency regression for the common non-German till. Called out
  in the code comment (`setup_page.go`) and here so it isn't silently
  rediscovered later.
- **F3 (fixed).** Used the existing `tseProvisionCountry` constant
  (`setup_tse.go`) instead of a second, undocumented `"DE"` literal, and
  updated `countryTaxLocale`'s maintainer NOTE (`setup_tax_catalog.go`) to
  say this hardcode also needs to move on the day a second tax-mapped
  country is added — it previously only mentioned the step-gating ternary.
- **F4 (disclosed here, not a code change).** The card's acceptance
  criterion "a country with no `countryTaxLocale` entry still renders no
  tile (no regression)" is **superseded, not literally satisfied**, by
  design: the tile now renders in the DOM for every country (Alpine, not
  the server, is what keeps it off-screen for a non-DE pick — see the
  fix's own reasoning above). The unit-level invariant it actually asks
  for — an unmapped country's `setupInstallableTaxPlugin` call still
  returns `nil, false` — is still true and still covered
  (`TestSetupInstallableTaxPlugin_UnmappedCountryReturnsNilNotUnavailable`),
  but that test can no longer fail as a consequence of anything
  `renderWizard` itself does. Recorded here explicitly so this AC isn't
  read later as "met" rather than "superseded by the fix's own design."
- **F5 (out of scope — filed separately, ut-docs#1468).** `web/help/en/
  users.md`'s step-3 description still calls the tax plugin "entirely
  optional and never installs itself" — the exact phrase the ut-docs#1180
  review already forbade from product copy (ADR-0048's hard sales gate: a
  live German shop cannot complete a real sale without it). Pre-existing,
  introduced by #1180, not by this change, so not fixed under this card —
  filed as a new Backlog card per the standing "manual is never behind the
  product" rule.

## Tests

`internal/pages/setup_tax_catalog_test.go`:
- **New** (TDD — written first, confirmed failing against the pre-fix
  code with the real assertion message below, then passing after the
  fix): `TestSetupWizardTaxPluginTilePresentEvenWhenOSCountryIsNotDE` —
  forces OS locale to `en_GB.UTF-8`/`Europe/London` (the pilot tablet's
  exact condition), confirms `GET /setup` detects `country: 'GB'`, and
  asserts the DE tax-plugin tile's markup (`action="/api/setup/tax-plugin"`)
  is present in the DOM anyway.
- **Updated**: `TestSetupWizardShowsTaxPluginInstallTileForDEOnly`'s
  FR-country half previously asserted the tile's markup must be **absent**
  from an FR re-render's HTML — no longer the correct invariant, since the
  tile is now unconditionally resolved for DE regardless of the posted
  country. Inverted to assert the tile **is** present (proving the fix's
  own "unconditional in the DOM" design) and, per F1 above, additionally
  pins both Alpine ternaries that are now the sole thing keeping it off a
  non-DE operator's screen.

Ran: `go build ./...`, `go vet ./...`, `gofmt -l .` (clean), full
`go test ./...` (all packages green), `bash scripts/ci/guard-data-access.sh`,
`bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-help-topics.sh` (all
pass — no i18n/help-topic changes needed, no new user-facing string, no
route change).

**Not done, deliberately (real-but-accepted gap):** a Playwright e2e spec
driving a live browser through the wizard. No e2e harness for the
first-boot setup wizard exists yet to build on (`e2e/tests/` has 17 specs,
none touch `/setup`; the existing harnesses start from an already-set-up
till) — building one from scratch is genuinely new infrastructure,
disproportionate to a `complexity:medium` one-line server-side conditional
fix that touches no client-side/Alpine/CSS code at all. The handler-level
tests render and assert against the exact HTML bytes a real browser would
receive, including the forced-OS-locale repro of the reported bug. Manual
verification on a real device is likewise out of scope for a cloud pipeline
session — nothing about this fix is device-, timing- or hardware-dependent.

## Safe to merge

Yes — independent review's fixable findings (F1, F3) applied and re-tested;
F2 and F4 are documentation obligations, satisfied by the comments above and
this record; F5 filed as its own card (ut-docs#1468), out of scope here.
