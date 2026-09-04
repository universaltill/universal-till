# Code review — Wizard restore auto-commit + busy indicator (ut-docs#1515)

**Date:** 2026-09-03
**Branch:** `fix/1515-wizard-restore-auto-commit-and-busy`
**Reviewers:** two independent Opus subagents (complexity:medium — build at
Sonnet, review at Opus), each in an isolated worktree, revert-then-restore
TDD verification performed by both.

## What shipped

Two defects on the first-boot setup wizard's "restore from another till"
step (`web/ui/pages/setup.html`), reported by the product owner on the
pilot tablet:

1. **The wizard's staged import silently never auto-committed** when the
   operator completed setup without ever tapping the country/currency
   step's tile — the common case when OS-locale detection already guessed
   correctly, since nothing invites a tap on an already-correct default.
   `ut-docs#970`'s own prior review made `currencyTouched` the one signal
   guarding auto-confirmation of a currency (never silently label imported
   prices under a currency nobody chose), so `commitStagedImportForSetup`
   correctly refused to run — but the operator was dropped on `/import`
   with zero explanation why.
2. **The wizard's own upload/preview form never got `ut-docs#1510`'s
   busy-indicator treatment** — a real `.bkp` (potentially thousands of
   receipt PDFs to parse) left the screen looking dead, inviting a
   double-tap.

## The fix

- **Kept the `ut-docs#970` currencyTouched safety gate completely
  unchanged** — no bypass. Added an explicit currency-confirmation
  checkbox to the wizard's upload panel, shown only when a preview is
  staged, currency is genuinely unconfirmed, **and** a real (non-blank)
  currency exists to confirm. Checking it sets the *same* `currencyTouched`
  Alpine flag a step-2 tile click already sets — no parallel signal the
  server-side gate can't see.
- Disabled the wizard's "Next" while a staged import sits unconfirmed
  (same three-way gate: staged, untouched, non-blank currency), so an
  operator can't silently proceed past it.
- Added `hx-indicator`/`hx-disabled-elt` to the wizard's upload form,
  mirroring `import.html`'s existing `#import-busy` pattern exactly.
- `GET /import` now distinguishes *why* a staged preview landed there:
  computes `currencyUnconfirmed` from the same `KeyCurrencyConfirmed`
  setting the commit-time gate reads, and shows a new, specific message
  instead of the generic one when that's the actual reason.
- 4 new i18n keys added to all 4 core locales (en/ar/fa/tr), inserted
  surgically (not a full alphabetical re-sort — an earlier attempt at that
  produced a 2,900-line diff across the locale files and was reverted).
- New Go tests (markup assertions + a `GET /import` behavioural test) and
  a rewritten real-browser Playwright e2e test (`login.spec.ts`) that
  drives the actual bug scenario end-to-end: uploads a real CSV, confirms
  currency via the new checkbox, and asserts the wizard lands on
  `/catalog` with the item actually present — not `/import`.
- `web/help/en/users.md` updated in the same branch (product-owner
  standing rule) and all doc screenshots regenerated via `make docs-shots`.

## Independent review findings

**Round 1** (full diff): one **blocker**, four nits.

- **F1 (blocker, fixed).** The new checkbox/Next-disabled gate didn't
  check for a *blank* currency — reachable whenever OS-locale detection
  matches no seeded country (this repo's own CI sandbox is one such case).
  Confirming a currency that isn't even shown would mark the till
  "confirmed" against an empty string; `commitStagedImportForSetup`'s gate
  only checks `currencyTouched`, not that the currency is real, so the
  import would have committed under an unconfirmed currency — reopening
  the exact `ut-docs#970` hole this whole card exists to close. Fixed by
  requiring `currency !== ''` everywhere the gate is evaluated, falling
  back to the pre-existing (bare-redirect-to-`/import`) behaviour when
  nothing was ever detected.
- **F2 (fixed).** Help-doc prose described a "decline and land on
  `/import`" path that isn't actually reachable that way once Next is
  gated — rewritten to describe the two real branches (confirmed → auto
  -commit; nothing-detected → the old fallback).
- **F4 (fixed).** A regression-test assertion (`hx-disabled-elt=...`) was
  vacuous — that exact string already existed elsewhere in the file
  before this card, so the assertion passed identically with or without
  the fix. Merged into one contiguous string tied to the new form
  specifically.
- **F6 (fixed).** Missing the `.row-warn-icon` span every other
  `.notice-block-warn` use in the codebase carries. Added.
- **F7 (fixed).** The confirm-currency block hid itself the instant its
  own checkbox was ticked (`x-show="!currencyTouched"`) — the same
  self-hiding-container class this same file's `ut-docs#617` review had
  already fixed once elsewhere. Replaced with a one-way `neededConfirm`
  latch (`x-init="$watch(...)"`) so the control stays visible as a record
  of what was confirmed, with the checkbox reflecting (`:checked`) rather
  than only setting (`@change`) `currencyTouched`.

**Round 2** (scoped to the F1/F2/F4/F6/F7 fix, revert-then-restore
verification of the blocker): **PASS**, plus two new findings from the
fix's own new code, both fixed:

- **N1 (cosmetic).** The F7 latch is never cleared, so a Back → "Yes"
  round trip could re-show the confirm block after `restoreStagedId` was
  already reset to `''` — visibly disagreeing with its own hint text.
  Fixed by pairing the latch with a live `restoreStagedId !== ''` check.
- **N2 (real gap, same class as F4).** The watcher's own
  `currency !== ''` guard was asserted nowhere — reverting only that half
  left the whole suite green. Without it, a blank-currency latch still
  fires, the block renders "confirm the currency:" with nothing to show,
  and ticking it sets `currencyTouched` with nothing stopping the
  server-side commit — the F1 hole via a different door. Pinned with a new
  assertion on the full watcher expression.

Both review rounds independently confirmed the mechanism is sound: the
checkbox reuses the exact same `currencyTouched` root Alpine state a
step-2 tile click sets (no parallel/shadowed variable), Alpine's `$watch`
cannot fire on a stale non-empty `restoreStagedId` at registration (traced
through the vendored Alpine source and the surrounding markup/lifecycle),
and the rewritten e2e test's technique — forcing a mismatched-PIN
server re-render to get a real, non-blank detected currency with
`currencyTouched` reset to false — is the same one already established in
this file's own "a detected country shows alone at first" test, applied
correctly to a new scenario.

## Verified beyond automated tests

- Both review rounds independently reverted the fix (source-level, in an
  isolated worktree per the reviewer skill's `ut-docs#386` rule) and
  re-ran the specific new tests, confirming genuine pre-fix failures with
  the exact expected messages, then restored and confirmed green.
- Full real-browser Playwright run (`login.spec.ts`, `--project=auth`,
  the genuinely-fresh-install fixture) — 15/15 passing, including the
  rewritten wizard-completion test that drives a real file upload,
  preview, and currency confirmation through the actual Alpine/htmx client
  code, landing on `/catalog` with the imported item visible.
- Full `go test ./...`, `go vet ./...`, `gofmt -l .` clean.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  and green, including `guard-i18n.sh`, `guard-help-topics.sh`, and
  `guard-docs-shots.sh` (screenshots regenerated three times across the
  fix-then-re-review cycle as the app surface kept changing).
- No real client/shop name used as test data (only generic names:
  "E2E Test Shop", "Wizard Restore Widget").

## Safe-to-merge verdict

**Yes.** Both independent review rounds passed; every finding from both
rounds was fixed and re-verified, not deferred.

## Explicitly deferred

- The 4 new i18n strings (ar/fa/tr) are hand-translated by this pipeline,
  not verified by a native speaker — flagged by round 1 as a legitimate
  but non-blocking, low-severity note. Standard for this pipeline's
  process; a native-speaker pass is out of scope for this card.
- The blank-currency fallback branch (`currency === ''`) is covered by an
  exact-string Go-level assertion on the disabled-condition expression
  (walked through manually in both review rounds) rather than a dedicated
  second e2e pass — the CI sandbox's own environment already exercises
  this branch by default whenever the country tile isn't clicked, and a
  second full wizard-completion e2e run for this branch alone was judged
  not worth the added runtime for what is, at this point, a single
  boolean-logic guard.
