# Code review — §6 KassenSichV TSE evidence on receipts (ut-docs#585)

**Date:** 2026-08-15
**Card:** universaltill/ut-docs#585 (p1, compliance, `complexity:hard` — escalated from `medium` at pick-up; see the issue comment for why)
**Branch:** `feat/tse-receipt-fields-585`
**Dev:** subagent, model `fable` (hard-tier build model)
**Reviewer:** independent subagent, model `opus`, isolated worktree (hard-tier review model — deliberately not `fable`, so the review isn't the same reasoning checking its own work)

## What shipped

`fiscal.sign.ask` (ADR-0044, ut-docs#675) is a real tender-phase extension
point whose `approved` response previously carried zero TSE evidence. This
card extends it additively — contract bump `fiscal-sign-ask.md` 1.0.0 →
1.1.0 (companion PR in `ut-docs`) — so an `approved` answer may optionally
carry a `tse` object mapping 1:1 to the §6 KassenSichV receipt requirement:
TSE transaction number, signature counter, TSE serial number, transaction
start/log time, the signature itself, and the signing algorithm. The field
set is modeled on the **legal requirement**, not on any vendor's API shape
— no fiskaly sandbox or real TSE exists in this codebase yet
(`ut-plugin-tax-fiskaly`, ut-docs#757, is unbuilt), the same "no sandbox
creds" constraint #757 already established and documented honestly rather
than faked.

- `internal/pages/fiscal_sign_hook.go` — `fiscalTSEEvidence` (all fields
  `omitempty`), threaded through `fiscalSignResult.Evidence`, populated
  only when the answer's `signature` is non-empty
  (`hasSignature`) — a bare or signature-less `approved` stays exactly the
  same clean approval it always was. No outcome changes: no new marker,
  no retry-queue entry, for any shape of evidence.
- `internal/db/migrations/048_fiscal_tse_signatures.sql` +
  `internal/data/fiscal_repo.go` — one row per signed sale, keyed on
  `sale_id`, `INSERT … ON CONFLICT(sale_id) DO NOTHING` (idempotent —
  first write wins; a background-retry re-sign of an already-recorded sale
  never duplicates or overwrites).
- `internal/pages/pos_api.go` — persistence wired into `completeTender`
  next to the existing `declareUnsignedFiscalSale`/`unsigned_override`
  blocks (best-effort, log-only — never unwinds an already-committed
  sale); `renderReceipt` gained a `*data.FiscalTSESignature` parameter,
  fetched at the receipt-HTML call site the same per-sale way the existing
  `unsignedFiscalSigning` flag already is.
- `web/ui/partials/receipt.html` — new conditional block, same
  `.receipt-legal` shape as the existing `UnsignedFiscalSigning` notice;
  per-field skip-if-empty (never placeholders), plus a QR code
  (`skip2/go-qrcode`, the same embed pattern `settings_page.go`/
  `sync_api.go` already use).
- `internal/pages/print_api.go` — ESC/POS path appends plain-text `Meta`
  lines for the same evidence (no QR on that path — declared non-goal).
- i18n: 8 new `receipt.fiscal.tse.*` keys, all 4 locales.
- Help: `web/help/*/sell.md` — new section on the TSE signature block.
- Tests (TDD-first): repo round-trip/idempotency, `fiscal_sign_hook_test.go`
  evidence-parsing table plus a full wazero-runtime e2e test (new fixture
  `testdata/fiscalsign_tse_guest`) proving persist+render of real values
  and that a no-signer sale renders neither placeholders nor the block,
  `print_api_test.go` ESC/POS lines, retry-tick persistence.

## Independent review (Opus, isolated worktree) — findings

**1 BLOCKING, fixed.** The manual (`web/help/{en,fa,ar,tr}/sell.md`)
described the TSE block as showing "the data German receipts are required
to show" — a compliance-**outcome** claim (the field set is legally
sufficient), not a capability description, and inconsistent with the rest
of the diff's careful "modeled on the requirement, not verified against a
real TSE" honesty (ADR-0040's rule, and `guard-compliance-claims.sh`'s own
stated scope: "this checks a denylist, it is not a copy reviewer" — this
was exactly the gap that denylist can't catch). It also presented the QR
code flatly, with no hint it's a provisional, unverified payload format.
**Fixed**: reworded all 4 locales to describe what is shown (the signing
details the plugin returned) rather than asserting legal completeness, and
added a plain disclosure that the QR format is provisional pending
confirmation against a certified TSE. Re-ran
`guard-compliance-claims.sh`, `guard-help-topics.sh`, and regenerated
screenshots (`make docs-shots` — the `sell` topic's markdown hash changed,
which the surface hash requires) — all green.

**9 non-blocking**, evaluated and accepted as documented, deferred, or
correct-as-is (not re-litigated in full here — see the review subagent's
own report for detail): the new `lang-pack-drift` external-pack follow-up
(handled — see below, not left as a dangling TODO); evidence-persist
failures being silent to the operator vs. the signing-failure path's
audit+Problems-list treatment (real asymmetry, filed as a follow-up
Backlog card rather than fixed here — expanding the persistence-failure
surface was judged out of this card's own scope); no test exercises the
*real* migrated schema directly (every test hand-rolls the table) — the
reviewer independently verified the real migration's idempotency by hand
(see below) and this is noted as a coverage gap, not a defect; `0` treated
as "absent" for the two integer counters (real TSE counters start at 1,
low practical risk, worth a contract note); QR module size is tight at
140×140px for a realistic payload (fold into the provisional-QR
follow-up); `renderReceipt`'s parameter count (~18 positional) is past
where a struct is warranted — noted, not restructured in this diff; a
pre-existing local-variable shadow of the `data` package inside
`renderReceipt` (compiles fine, cosmetic).

## What the reviewer verified beyond reading

- `go build`/`go vet` clean; full `internal/pages`, `internal/data`,
  `internal/db` suites green (123.8s / 26.3s / 3.7s).
- All 6 required guards green (`guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-i18n`, `guard-compliance-claims`,
  `guard-help-topics`) — re-run again by the orchestrator after the B1 fix,
  still green, plus `guard-docs-shots` after `make docs-shots`.
- **TDD re-verified independently**, not taken on the dev subagent's word:
  two separate reverts (dropping the evidence assignment in
  `askFiscalSign`; disabling the `tseView` build in `renderReceipt`), each
  producing a real, non-tautological failure (`expected the evidence
  carried through, got nil`; the receipt HTML missing the expected value),
  then restored to green.
- **Idempotency claim tested against the real migration**, not just the
  hand-rolled test schema — a throwaway test against `048_...sql` applied
  for real confirmed `ON CONFLICT(sale_id) DO NOTHING` genuinely gives
  first-write-wins, and that the migration is the correct next number
  (048) and is picked up automatically (no registry to update).
- **Screenshot nondeterminism claim empirically confirmed**, not just
  trusted: ran `make docs-shots` twice more against the unmodified
  committed tree — the same unrelated topics (`alerts`, `designer`, and
  intermittently `users`/`invoices`) churn on every run in all 4 locales
  with zero code change, while the manifest's `surface_sha256` stayed
  byte-stable across all three runs. Confirms the dev's explanation; not
  masking a real defect.
- Money: `transaction_number`/`signature_counter` confirmed `int64`
  end-to-end, correctly never `money.Money`.
- i18n: exactly 8 new keys, present and consistent across all 4 locale
  files; the ESC/POS path's English literals are a deliberate, pre-existing
  convention (that renderer's other lines are also English), not a gap.
- UX: logical CSS properties only, no hardcoded colors, `overflow-wrap`
  handles the long base64 signature, longer fa/ar/tr labels wrap without
  breaking layout, no new checkout/kiosk modal blocker.
- Secrets/demo data: no real client/shop name, no credential-shaped
  literal anywhere in the diff.

## Follow-up work filed, not built here (non-goals, stated up front)

- `ut-plugin-tax-fiskaly` (#757) will need its own follow-up to actually
  populate the new v1.1.0 `tse` fields from a real fiskaly response —
  different repo, doesn't exist yet.
- ESC/POS QR rendering (raster/native QR support on that print path).
- Confirming the QR payload's exact byte format against the authoritative
  TSE-QR-code convention or a real TSE vendor/sandbox — needs fiskaly
  access or a tax advisor, neither available to a cold cloud cycle.
- The evidence-persist-failure observability gap (non-blocking finding
  above) — filed as a new Backlog card.
- `fiscal.tse_failing_since` (ADR-0048) — untouched, out of scope, as the
  base contract already documented.

## Companion changes landed alongside this one

- `ut-docs` — contract bump `fiscal-sign-ask.md` 1.0.0 → 1.1.0 (separate
  PR in that repo).
- `ut-plugin-language-de` / `ut-plugin-language-es` — the 8 new
  `receipt.fiscal.tse.*` keys, real translations, so `main`'s blocking
  `lang-pack-drift` push check doesn't red-X after this merges (own PRs,
  own review records in each repo).

## Safe-to-merge verdict

Yes, after the B1 fix. Guards, build, full package tests, and two
independent TDD re-verifications all green.
