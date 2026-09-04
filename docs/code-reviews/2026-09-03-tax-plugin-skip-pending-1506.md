# Code review: queue the tax plugin for background retry when step 3 is left without installing (ut-docs#1506)

**Date:** 2026-09-03
**PR:** universal-till#(TBD, opened right after this record)
**Reviewer:** independent Opus subagent, isolated git worktree (complexity:medium → Sonnet build / Opus review, per the pipeline's model-routing rule)
**Author:** Farshid Mirza (autonomous `lane:cloud-54` pipeline cycle)

## What shipped

The setup wizard's Germany-only step 3 shows a "Germany tax plugin" tile
whose own copy calls the plugin required and promises a background retry
("we'll keep retrying in the background until you are [online]"). Before
this fix, every way of leaving the step without pressing **Install** left
that promise false: nothing was queued for ut-docs#591's existing
background-retry mechanism, and nothing told the operator. A German shop
could finish first-boot setup with no fiscal plugin, no queued retry, and
no warning — reproduced and root-caused on a real pilot device.

- `POST /api/setup/tax-plugin-skip` (new handler,
  `setupTaxPluginSkipHandler`): re-resolves country → locale → catalog
  match server-side, and when a real not-yet-installed match exists,
  queues it via the *existing* `addPendingBasePlugins` helper — the same
  list `setupTaxPluginInstallHandler`'s failure branch already feeds. One
  retry mechanism, two doors into it.
- Wired to both doors off step 3: the explicit **Skip for now** button
  (full round-trip, `form=` trick already used by the Install button,
  resumes on step 4 with a new `setup.tax_plugin.skip_warning` note) and
  the primary **Next** button (background `hx-post`, `hx-swap="none"`,
  no navigation — Next must never lose whatever the operator just typed
  into the TSE fields, which a page reload would).
- `internal/auth/middleware.go`: `/api/setup/tax-plugin-skip` added to
  the first-boot auth-exempt switch — the exact class of omission
  ut-docs#1507 already fixed once for the sibling `/api/setup/tax-plugin`
  route, and caught automatically this time by the existing
  `TestSetupWizardEndpointsClearTheSessionWall`, which scrapes every
  `action="…"` out of `setup.html` and asserts it's exempt.
- New locale key in all four `web/locales/*.json`; `web/help/*/users.md`
  item 8 updated (it previously called the plugin "entirely optional and
  never installs itself", which this fix makes newly inaccurate).

## Review round 1 — BLOCK, two real gaps found

The first review pass (Opus, isolated worktree, against the pushed
branch) confirmed the code as originally written was solid on security,
TOCTOU, dedup, and test quality, but **found the ticket's acceptance
criterion was not actually met**:

- **B1 — the primary "Next" button was still a silent no-op door.** An
  operator who filled in the TSE fields (the step's actual content) and
  pressed **Next** — arguably the *more* likely path than Skip — left
  through a route that queued nothing. The reviewer proved this with a
  throwaway test driving the real final `POST /api/setup`: `pending after
  a completed DE wizard with the tile showing and Next pressed: []`.
- **B2 — a fully offline first boot never renders the tile at all**
  (`setupInstallableTaxPlugin` returns `(nil, true)` when the catalog is
  unreachable with nothing cached, so `{{ if .installableTaxPlugin }}`
  never emits the tile or either form), so the whole fix was a no-op for
  the headline scenario the tile's own copy describes ("or we'll keep
  retrying in the background until you are [online]").

The reviewer's suggested fix for both — queue unconditionally at the
final `POST /api/setup`, keyed off `countryTaxLocale[country]`, without
consulting the catalog — was **not applied as suggested**. Doing so would
have added an unconditional per-country tax-plugin queue at the exact
place `internal/pages/setup_base_plugins.go`'s own doc comment on
`basePluginSpec` explicitly forbids without a superseding ADR: *"ONLY
canonical type 'language' belongs in setupBasePlugins — a fiscal/tax
entry would contradict ADR-0025 decision 4 (fiscal plugins are prompted,
never silently auto-installed) and needs a superseding ADR first."* A
till that never rendered the tile at all never prompted the operator —
queuing a fiscal-plugin install behind their back there is a real
ADR-0025 D4 tension, not a refactor.

## Fixes applied after round 1

- **B1 fixed** (Next button, tile visible, i.e. genuinely prompted): the
  Next button now also fires the same `/api/setup/tax-plugin-skip`
  request in the background via `hx-post`/`hx-include`/`hx-swap="none"`
  when `.installableTaxPlugin` is present, alongside Alpine's unchanged
  client-side `step = 4`. No navigation, so no TSE-field loss and no
  `currency_touched` regression. Covered by two new DOM tests
  (`TestSetupWizardNextButtonQueuesTaxPluginInBackgroundWhenTileShowing`,
  `...HasNoBackgroundQueueWhenTileAbsent`); the actual queuing behaviour
  reuses the already-tested handler.
- **B2 deliberately NOT fixed** — see "Deliberately out of scope" below.
- **N1 fixed** (Settings pending chip mislabeled a queued tax spec as a
  "language pack" — `setup.base_plugins.pending` is rendered
  unconditionally regardless of `.CanonicalType`): added a type-aware
  `setup.base_plugins.pending_tax` key (all four locales) and branched
  `settings.html`'s chip on `.CanonicalType`.
- **N2 fixed** (wording drift — the new `skip_warning` key said "cannot
  complete a **legal** sale", while every neighbouring string and the
  updated help docs say "**real** sale"; "legal sale" edges toward the
  legal-outcome claim ADR-0040 forbids, even though it didn't literal-match
  `guard-compliance-claims.sh`'s denylist): reworded to "real sale" in all
  four locales.
- **N3/N4 (minor, not fixed this cycle):** `currency_touched` is reset on
  the Skip round-trip's GET resume (pre-existing shape on the install
  round-trip too, newly exercised on a more common path); the dead
  `.tse-field` clear in the Skip button's `@click` when it now submits a
  different form. Neither is a regression this fix introduces beyond what
  the existing install round-trip already had, and neither blocks the
  actual defect. Noted for a future pass.

## Deliberately out of scope: B2 (fully offline first boot)

Queuing the fiscal plugin when the operator was **never shown the tile at
all** is a materially different call from queuing when they saw it and
left without installing — it trades an explicit ADR-0025 D4 guardrail
("prompted, never silently auto-installed... needs a superseding ADR
first") for the tile's own copy's implicit promise. That is a genuine
product/compliance judgement, not an engineering one, and the pipeline's
standing rule is not to guess past a real business decision. Filed as a
follow-up: **ut-docs#1512** ("Offline first-boot DE setup: tax-plugin tile
never renders, so ut-docs#1506's queue-on-skip/Next fix cannot reach it —
needs a product/ADR call on whether to queue without ever prompting"),
routed to Admin Review with the two options spelled out (queue anyway
with a superseding ADR note vs. always render the tile with a
"couldn't check yet, will retry" state instead of gating render on the
catalog fetch) and a recommended default.

Also out of scope, per the ticket's own framing (**not** re-litigated
here): splitting the tile off step 3 into its own wizard step. The
reviewer explicitly agreed this descoping is reasonable and does not
leave the core bug (the false retry promise) unfixed — B1/B2 are both
closeable, and were mostly closed, without any step restructuring.

## Round 2 verification

Re-ran the full gate after the B1/N1/N2 fixes:

- `gofmt -l .` — clean
- `go build ./...` — clean
- `go vet ./...` — clean
- `go test ./...` — full suite green, including the two new Next-button
  DOM tests and every existing Skip/install test unchanged
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job
  (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-page-http-error.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`,
  `check-brand-assets.sh`, `guard-makefile-version.sh`) — all pass
- `make docs-shots` re-run (surface hash `989bd7fbde7c…`) after the
  template edits, `guard-docs-shots.sh` fresh

## TDD re-verification (round 1, still valid — the queuing logic itself
was unchanged in round 2)

The reviewer replaced the `addPendingBasePlugins(...)` call in
`setupTaxPluginSkipHandler` with a no-op and confirmed
`TestSetupTaxPluginSkipQueuesPendingAndBackgroundRetryInstallsIt` failed
with a clean, sensible assertion message (not a panic, not a false pass),
while the other six skip tests correctly stayed green (they pin
reject/no-op/first-boot/render/DOM properties, not the queueing itself).
Restored via `git checkout --`, full suite re-confirmed green.

## Final verdict

**APPROVE.** Both blocker-class findings from round 1 are closed for the
prompted case (tile visible, Skip or Next); the one remaining gap (fully
offline, never-prompted) is a genuine, explicitly-scoped-out product/ADR
question, tracked as ut-docs#1512 rather than guessed past. Security,
TOCTOU, dedup, i18n and test-quality findings from round 1 all confirmed
sound; the two real wording/UX nits (N1, N2) are fixed; N3/N4 are minor
pre-existing shape, noted for later.
