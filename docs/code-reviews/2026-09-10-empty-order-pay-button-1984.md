# Code review: empty-basket Pay button (ut-docs#1984)

**Date:** 2026-09-10
**Branch:** `fix/1984-empty-order-pay-button`
**Card:** ut-docs#1984 (split from ut-docs#1965's Figma dark/amber design, part b)
**Complexity:** easy

## What shipped

The sale screen's `.payment-trigger` button (`data-testid="payment-open"`,
`.tender-default-footer`, `web/ui/pages/index.html`) previously showed a
static "Payment" label regardless of basket state and was always
clickable, even with a genuinely empty basket. It now:

- Shows **"Pay £x"** (new locale key `tender.pay_with_total`, formatted
  via the existing `money` template func) once the basket has at least
  one line.
- Shows **"Add items to pay"** (new locale key `tender.pay_empty`) and is
  a genuinely `disabled` HTML button — not just relabelled — while the
  basket is empty.
- Renders correctly on **first paint**, with no flash from a placeholder
  state: `internal/pages/index_page.go` now reads the current basket's
  `ItemCount()`/`Total` via the already-existing `d.Engine.Basket()` call
  (the same one `update_api.go` uses for its own empty-basket check),
  guarded against a nil `Engine` for test harnesses that don't wire one
  (`TestBackofficeModeRedirectsHome`).
- Stays in sync afterward via a small page-local IIFE listening on
  `htmx:afterSwap`, reading `#basket`'s own `.basket-count` text and
  `.total[data-minor]` attribute — the same data-attribute-bridge pattern
  the existing fee-hint and `pay-voucher-btn` scripts already use for an
  element that lives outside the swapped `#basket` fragment.

New locale keys added to `web/locales/{en,ar,fa,tr}.json` (all four
core-shipped locales — `guard-i18n.sh` enforces the full key set match).
This is a **brand-new** key pair, so the external
`ut-plugin-language-{de,es}` packs could not be updated ahead of core
(their own key-drift guard rejects a translation with no matching core
key yet, per `universal-till/CLAUDE.md`'s lang-pack-drift note). Their
own follow-up PRs (translating the same two keys, `manifest.json` version
bumped in each) are queued in this same pipeline cycle to land
immediately after this merges — `main` will show `lang-pack-drift` red in
the interim, which is the documented expected state for a brand-new key,
not a mistake.

`web/help/{en,de,fa,ar,tr}/sell.md`: the manual's own "how to use it"
step used to say "tap **Payment**" — a now-incorrect fixed label. Updated
to describe the button as "Pay" and explain the empty/non-empty label
swap, in all five locale topics (including `de`, a manual-only locale
per `checkhelptopics/main.go`'s `manualOnlyLocales`). `make docs-shots`
regenerated the `sell` topic's screenshots (all 4 core locales) plus
`till-designer`'s `ar` shot (unrelated pixel-rendering noise from the
same regeneration run, not a functional change — no file touching that
topic's surface was changed).

## Independent review

Fresh-context Sonnet subagent (per `complexity:easy` routing — Dev ran
inline at Sonnet, review at a fresh Sonnet instance that never saw the
implementation reasoning), briefed with the full diff, the acceptance
criteria, and instructed to actually run build/test/guards rather than
just read.

**Verdict: SAFE TO MERGE.** No blocking findings. Checked and confirmed:

- The nil-`Engine` guard is correct and necessary (verified
  `TestBackofficeModeRedirectsHome` genuinely builds a `Deps{}` with no
  `Engine` set, and `pos.Service.Basket()` unconditionally locks a mutex
  that nil-panics without the guard) — matches the codebase's existing
  `d.KioskEngine` nil-check convention (`update_api.go`).
- The JS refresh IIFE has no race: it re-reads state fresh on every
  `htmx:afterSwap` rather than caching, correctly deliberately omits a
  `DOMContentLoaded` listener (the server already renders correctly at
  first paint, and `#basket` doesn't exist in the DOM until the first
  swap anyway), and correctly handles both empty→non-empty and
  non-empty→empty transitions.
- i18n: all four locale files carry both new keys with matching key sets
  (no drift), `tender.pay_with_total` contains `%s` in every locale.
- Every e2e spec's added `/api/pos/reset` or scan-first step was checked
  against the file's actual `describe`/`beforeEach`/`afterEach` brace
  nesting, not assumed from the commit message — confirmed
  `tab-bar-overflow-aria-424.spec.ts`'s two edited tests are siblings of
  (not nested inside) the inner `describe` that owns its own
  `afterEach`, so they were genuinely uncovered before the explicit
  reset was added; every other touched file already had adequate
  file/block-level hooks.
- One pre-existing, unrelated observation (not a regression from this
  diff): `payment-overlay-focus-sweep-1702.spec.ts`'s held-chip test
  leaves a held sale in storage that `/api/pos/reset` doesn't clear —
  backstopped by `e2e/README`'s own between-files basket-reset fixture
  regardless, and the hold-related code itself wasn't touched here.
- One non-blocker nit: the JS `fmt()` helper's `String.replace(str, str)`
  honors special replacement patterns (`$$`, `$&`) in the replacement
  argument — inherited unchanged from the pre-existing fee-hint/
  pay-voucher-btn scripts this file already used, not introduced by this
  diff, and currency-formatted strings are not a realistic trigger.

## Verified beyond automated tests

- `go build ./...`, `gofmt -l .` (clean), `go vet ./...` (clean),
  `golangci-lint run ./...` (0 issues).
- `go test ./...` — full suite green (including
  `TestBackofficeModeRedirectsHome`, which exercises the nil-Engine
  path this diff added a guard for).
- `scripts/ci/guard-i18n.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh`, `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-compliance-claims.sh`,
  `guard-page-http-error.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-plugin-menu-read.sh` — all
  green.
- Real Playwright/Chromium runs (not just `.evaluate()` geometry checks)
  against every e2e spec file that references `payment-open`/
  `payment-trigger`/`tender.open_payment` (21 spec files, 86 individual
  test cases across the runs) — all pass, including the full
  `sale.spec.ts`/`tender-panel-reachable.spec.ts` real-checkout flows
  and every focus/OSK/accessibility spec whose empty-basket setup needed
  a scan-first fix.
- `ut-plugin-language-{de,es}`: `scripts/validate.sh` clean;
  `scripts/check-key-drift.sh` run against this branch's local
  `en.json` (not stale `main`) reports 100% key coverage, 0 drift, 0
  orphans, for both packs; each pack's own self-test suites
  (`check-key-drift.test.sh`, `check-version-bump.test.sh`) pass;
  `check-version-bump.sh` confirms the `manifest.json` bump is covered
  correctly.

## Pre-merge supersession check (SKILL.md Lane ownership rule 7a)

- `git log main --oneline -- internal/pages/index_page.go web/ui/pages/index.html`
  since this branch's fork point: no independent fix landed.
- Linked issue ut-docs#1984: still open, `status:in-progress`,
  `lane:cloud-24` (this lane), no closing PR referenced elsewhere.
- Not superseded.

## Safe-to-merge verdict

**Safe to merge.** Merge method: `merge` (not squash/rebase), per
ut-docs#250. `Closes universaltill/ut-docs#1984` in the PR description.

## Explicitly deferred

- The `ut-plugin-language-{de,es}` pack follow-up PRs are separate PRs in
  their own repos (branches already pushed:
  `docs-1984-pay-button-empty-basket-keys` in both), to be opened/merged
  in this same pipeline cycle right after this PR merges — not deferred
  indefinitely, just sequenced after core per the reviewer skill's
  "brand-new key" ordering rule.
- The pre-existing `fmt()` replacement-pattern nit above is inherited,
  unrelated to this diff's own correctness, and not worth a standalone
  fix in this cycle.
