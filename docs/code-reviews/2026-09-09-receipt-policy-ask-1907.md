# Code review: three-way receipt policy (ut-docs#1907, ADR-0089)

- **Card:** universaltill/ut-docs#1907 — `always`/`ask`/`never` receipt
  policy replacing `printer.auto_print`, with a `receipt.policy.ask`
  extension point and an interim core-only Germany carve-out pending
  ut-docs#1908.
- **Design:** ADR-0089 (universaltill/ut-docs#1916, merged).
- **Complexity tier:** hard. Dev: Fable subagent (isolated worktree),
  implementing and testing, not committing. Review: Opus, independent,
  fresh context — per this pipeline's model-routing rule that the cheap
  model builds and the strong model reviews.
- **Reviewer:** Opus subagent, adversarial pass against the compliance-
  critical path specifically (the Germany carve-out and the plugin hook's
  untrusted-input handling), including two "revert and confirm red" TDD
  re-verifications.

## What shipped

- `printer.auto_print` (bool) → `printer.receipt_policy` (`always`/`ask`/
  `never`), resolved on read with a legacy-key fallback — no migration,
  bit-for-bit unchanged behaviour for an existing shop.
- New `receipt.policy.ask` extension point (`internal/pages/receipt_policy_hook.go`),
  the same generic `.ask` hook mechanism (ADR-0041) as `charge.policy.ask`/
  `tax.rate.ask`: a country-tax plugin may constrain the allowed policy
  set; no answer, a transient failure, or a malformed answer all mean
  unrestricted (ADR-0050 Decision 2 — a plugin's absence is never
  catastrophic).
- Interim core-only Germany carve-out (`receiptPolicyLockedForCountry`),
  forcing `always` regardless of the stored value or any plugin answer,
  mirroring the existing Turkey/service-charge carve-out precedent
  (ut-docs#962, `internal/pos/charge_policy.go`) — explicitly temporary,
  pending the open accountant question at ut-docs#1908.
- Sale-completion "ask" prompt in `receipt.html`: paper or none only —
  ut-docs#1603 (digital receipt) doesn't exist anywhere in this codebase
  (verified: no email/SMS integration under `internal/`), so no
  placeholder digital option was built. Reuses the existing
  `/api/print/receipt/{no}` reprint endpoint; no new endpoint.
- Settings UI: the checkbox becomes a three-way `<select>`; for a DE shop
  it renders `disabled` with a hidden `always` field and an explanatory,
  non-compliance-claiming note.
- i18n: 8 new keys across all 4 core locale files (en/ar/fa/tr);
  `settings.printer.auto` removed (zero remaining references). Manual
  updated in all 5 help locales (`web/help/*/printing.md`).
- 18 tests / 40+ subtests in `internal/pages/receipt_policy_test.go`.

## Independent review — adversarial checks against the compliance-critical path

The Germany carve-out and the plugin hook are the two places a bug would
have real consequences (a German till silently skipping a legally-required
receipt, or a hostile/buggy plugin answer taking down checkout), so the
review targeted them specifically rather than a uniform pass:

1. **Policy-resolution precedence** — verified by reading the execution
   order in `printerConfigChecked`, then empirically pinned by swapping the
   plugin-clamp and DE-carve-out steps and confirming
   `TestPrinterConfig_ReceiptPolicy_GermanyForcesAlways` went red on all
   three subtests, then restored.
2. **The DE carve-out is genuinely unconditional** — applied last on the
   read path (after the plugin clamp), fails *safe* under a settings-read
   error, rejected (not coerced) on the save path, and `keyPrinterReceiptPolicy`
   is read in exactly one place in the whole repo — so even a hand-edited
   settings row can't produce a non-`always` effective policy for a DE
   till. `TestSettingsPage_GermanyLockSurvivesFormReplay` confirmed to
   exercise the real registered mux, not a stub.
3. **`printReceiptAsync`'s auto-print gate is untouched**, and `print.Config{`
   is constructed in exactly one place in the repo — no second, unpatched
   path that could disagree with the resolved policy.
4. **The hook's untrusted-input handling** — malformed JSON, wrong JSON
   type, empty list, all-unknown values, a handler error: all degrade to
   unrestricted, never block a sale/save, and no panic is reachable
   (`clampReceiptPolicy`'s `allowed[0]` index is only reached under
   `ok==true`, which requires a non-empty validated slice).
5. **The prompt is structurally non-blocking** — the sale is committed
   server-side (`completeTender`) before `printerConfig` is even read in
   the tender handler, well before the receipt HTML (and therefore the
   prompt) exists.
6. **TDD claims re-verified**, not just trusted: neutering
   `receiptPolicyLockedForCountry` to always return false turned all four
   DE-specific tests red; moving the carve-out above the plugin clamp
   turned the ordering test red. Both reverted, tree confirmed clean.

Full detail (i18n completeness, compliance wording, `renderReceipt` call
sites, test-quality spot checks) in the reviewer's own report — no
BLOCKER found across any of it.

## Findings — fixed before merge

1. **`.receipt-ask` was missing from `receipt.html`'s `@media print` hide
   list**, and the prompt's failure-path JS hid the block *after* calling
   `window.print()` in both its `.then`/`.catch` branches — a shop on the
   `ask` policy whose reprint POST failed would print "Would you like a
   receipt? [Print receipt] [No receipt]" onto the customer's own paper.
   Fixed: `.receipt-ask {display: none;}` added to the print media block,
   and the JS now hides the element before calling `window.print()` on
   every path.
2. **The hook's non-memoization doc comment was factually wrong** — it
   claimed `receipt.policy.ask` is only asked from the settings read/save
   paths; in reality `printerConfig`/`printerConfigChecked` has 12 call
   sites including the tender/checkout handler, kitchen-ticket printing,
   EOD and invoice rendering. Corrected the comment to name the real scope
   and flag memoization as a genuine follow-up (tracked below) rather than
   a considered, harmless tradeoff — cost is zero today only because no
   plugin implements the hook yet.
3. Trivial: removed a now-dead `autoPrint=on` form field from an existing
   `print_api_test.go` case (asserts on `charset`, not on that field).

Re-verified after these fixes: `gofmt -l .` empty, `go build ./...` clean,
targeted `receipt_policy_test.go`/`renderReceipt`/settings-printer tests
green, `golangci-lint run ./internal/pages/...` 0 issues,
`guard-i18n.sh`/`guard-compliance-claims.sh` clean, `make docs-shots` +
`guard-docs-shots.sh` re-run (the print-media/JS fix touches
`internal/pages/**`'s screenshotted surface).

## Follow-ups filed (not blocking this merge)

- Memoize `receiptPolicyAskEvent` the way `pluginChargePolicyAsker` is,
  before any plugin actually answers `receipt.policy.ask` — every
  `printerConfig` call (including the checkout hot path) would otherwise
  pay a synchronous WASM dispatch plus an `event_dispatch` audit INSERT
  per call once one exists.
- The settings page doesn't reflect a *plugin's* restricted policy set
  (only the DE lock) — a forbidden choice yields a raw, unlocalized
  English 400. Unreachable today; fix when a plugin implements the hook.
- Fail closed (reject the save) rather than silently allowing a DE shop to
  store a non-`always` value when the settings read for the carve-out
  check errors — a stored-value inconsistency only, since the read path
  still forces `always` unconditionally regardless.

## Verified beyond automated tests

- Full `go test ./internal/pages/... ./internal/print/... ./internal/pos/...`
  run independently twice (once by the implementing Fable session, once
  by the orchestrating session) — both green, not trusted from a single
  run.
- `make docs-shots` (104/104 screenshots) + `guard-docs-shots.sh`, twice
  (once before the review fixes, once after — the print-media/JS change
  touches the screenshotted surface).
- Offline-first: every piece here — settings read/write (local SQLite),
  print (local hardware), the `.ask` hook (an already-installed,
  in-process WASM plugin) — has zero network dependency, same posture as
  the rest of the printer/checkout path.

## Not in scope (deliberately)

- Digital receipt delivery (ut-docs#1603) or any stub for it.
- `ut-plugin-tax-de` (or any plugin) actually answering `receipt.policy.ask`
  — gated on ut-docs#1908's still-open accountant question.
- `ut-plugin-language-{de,es}` — external packs; `lang-pack-drift` will
  advise on the 8 new keys on this PR, as designed, and needs its own
  follow-up card once this merges (the merging lane owns that follow-up
  per this pipeline's own standing rule).
