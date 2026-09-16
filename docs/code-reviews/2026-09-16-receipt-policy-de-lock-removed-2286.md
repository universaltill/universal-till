# Code review: remove the DE receipt-policy lock (ut-docs#2286)

**Date**: 2026-09-16
**Card**: ut-docs#2286 — product-owner decision, item 6 of the 2026-09-16
design-change batch. Carries the decision ut-docs#1908 was waiting for;
#1908 stays open for the accountant question but no longer blocks anything.
**Complexity**: easy. **Review model**: fresh-context Sonnet subagent
(per `MODEL-ROUTING.md`).

## What shipped

ADR-0089 Decision 3 (the interim core-only Germany carve-out forcing
`printer.receipt_policy = always` for `store.country == "DE"`) is
rescinded — documented as an addendum to `ut-docs/adr/0089-...md`. German
shops now choose freely among `always`/`ask`/`never`, same as every other
country:

- `internal/pages/print_api.go`: removed the read-side clamp
  (`printerConfigChecked`) and the save-side 400 rejection
  (`/api/settings/printer`).
- `internal/pages/receipt_policy_hook.go`: deleted
  `receiptPolicyLockedForCountry` (its only call sites were the two above
  plus the settings-page template flag).
- `internal/pages/settings_page.go`: `receiptPolicyLocked` template key
  replaced with `receiptPolicyAdvisoryDE` (true when `store.country ==
  "DE"`, drives a non-blocking advisory instead of a disabled control).
- `web/ui/pages/settings.html`: `<select>` is never disabled now; the
  hidden `receiptPolicy=always` input is gone; a DE-only advisory
  paragraph explains §146a Abs. 2 AO ("ask" satisfies the offer
  requirement; "never" needs a digital receipt or a §148 AO exemption) —
  no compliance-outcome claim (ADR-0040).
- i18n: `settings.printer.receipt_policy.locked_de` renamed to
  `...advisory_de` with new text, translated in all four bundled locales
  (en/ar/fa/tr) in this repo, plus follow-up PRs prepared for the external
  `ut-plugin-language-{de,es}` packs (landed after this PR merges, per the
  brand-new-key ordering rule — the pack's own key-drift guard treats an
  untranslated-on-either-side key as an orphan until core's `main` carries
  it).
- Help manual: `web/help/{en,de,ar,fa,tr}/printing.md` item 9 updated —
  no more "fixed to Always print" language.
- Tests: `receipt_policy_test.go`'s three DE-lock assertions
  (`GermanyForcesAlways`, `GermanyOnlyAcceptsAlways`,
  `GermanyLockSurvivesFormReplay`) replaced with their no-lock
  equivalents (`GermanyKeepsStoredValue`, `GermanyAcceptsAllPolicies`,
  `GermanySavesChosenPolicy`), plus a new
  `TestFiscalSignAsk_ApprovedRegardlessOfReceiptPolicy` proving the card's
  own acceptance criterion: every completed sale is still TSE-signed
  regardless of receipt policy (printing has always been architecturally
  downstream of `fiscal.sign.ask`, never a precondition of it).

## What the independent review found

**PASS, no findings — blocking or otherwise.** Full detail in the
reviewer's own report (fresh-context Sonnet subagent, isolated worktree);
summary:

- Grepped for every deleted symbol (`receiptPolicyLockedForCountry`,
  `receiptPolicyLocked`, `locked_de`) across the whole tree — zero live
  references remain.
- Confirmed the i18n key is consistent across the Go template data key,
  the HTML template, and all four locale files, and that the ar/fa/tr
  translations are real, coherent text (not placeholders or garbled
  machine output).
- Independently re-verified the TDD claim: reintroduced the deleted
  `disabled` attribute into `settings.html`, reran
  `TestSettingsPage_ReceiptPolicyControl` — failed with the expected
  "the control must not be disabled" message — then reverted and
  confirmed it passes again.
- Ran the full package test suite (`go test ./internal/pages/...`,
  321s, all green), `go build ./...`, `go vet ./...`, `gofmt -l .`,
  `golangci-lint run ./internal/pages/...` (0 issues).
- Ran `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh` — all green; confirmed
  the `printing` topic's drift lines are pre-existing baseline entries
  (ut-docs#332), not new drift introduced by this change.
- Read the actual help-manual prose in all five locales, not just the
  guard's exit code, and confirmed no stale "fixed to Always print"
  language survives anywhere.
- Checked for real client/shop names or secret-shaped literals — none.

## Verified beyond automated tests

- Fiscal signing is architecturally decoupled from receipt printing —
  confirmed by reading `pos_api.go`'s tender handler: `fiscal.sign.ask`
  resolves and `recordFiscalTSEEvidence` runs at the point payment is
  authorized, while `printReceiptAsync` fires only afterward, gated
  solely on `cfg.AutoPrint` (which the receipt policy sets, and nothing
  else reads). The new regression test exercises this end to end through
  the real `/api/pos/tender` handler with an installed wasm fiscal-signing
  plugin, for all three policies.
- Manually walked the settings-page render for a non-DE and a DE store,
  confirming the advisory only ever appears for DE and the control is
  never disabled in either case.

## Safe to merge

Yes. Build, vet, format, lint, guards and the full package test suite are
all green; the independent review found nothing to fix.

## Explicitly deferred / follow-up

- ut-plugin-language-de / ut-plugin-language-es translations for the
  renamed key are prepared (working tree, not yet pushed) — landing
  immediately after this PR merges, in the same cycle, per the "brand-new
  key: merge core first" ordering rule (`reviewer` skill's own note on
  this).
- Per the 2026-09-16 design-change batch note on this card: **no
  `release.yml` dispatch for this merge** — it ships as part of the
  batch's single `v0.17.0` minor release once the till-side batch cards
  are in.
- ut-docs#1908 (the accountant's §146a Abs. 2 AO question) stays open in
  Admin Review — this card does not answer it, only removes the interim
  lock that depended on it. Already linked via the product owner's own
  comment on #1908 when #2286 was filed.
