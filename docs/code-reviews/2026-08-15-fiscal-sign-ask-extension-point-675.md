# Review: fiscal.sign.ask tender-phase extension point (ut-docs#675)

**Date**: 2026-08-15
**Card**: universaltill/ut-docs#675 — "Implement fiscal.sign.ask: register
the ADR-0041 extension point + contract" (narrowed to core-only this
cycle; the `ut-plugin-tax-de` → `ut-plugin-tax-fiskaly` plugin split moved
to universaltill/ut-docs#757, blocked on this card, since no fiskaly
sandbox credentials exist in this environment)
**Complexity**: hard
**Dev model**: Fable subagent (two passes — initial implementation, then a
scoped fix pass for review findings)
**Reviewer model**: Opus subagent, fresh context (two rounds — initial
full review, then a scoped re-review of the fix pass)

## What shipped

Registers `fiscal.sign.ask` — the ADR-0044 Decision 1 / ADR-0041-shaped
tender-phase extension point — into `completeTender`
(`internal/pages/pos_api.go`): dispatched after the `payment.<key>.
authorize` loop resolves (payable total final, including any
reader-reported tip) and before `pos.CompleteSale` persists the sale.
`exclusive` point, 3000ms tender-phase budget (independent of the
authorize loop's own 2s/10s deadline), known-offline short-circuit on both
the cashier and self-order-kiosk tender paths.

On any failure (timeout, plugin-declared `unreachable`, or known-offline)
the sale completes anyway — never blocked, orthogonal to the existing
ADR-0048 hard gate — but is declared via the `proceed-and-declare` surface
(ADR-0041 Decision E): a `sale/unsigned_fiscal_signing` audit marker
(mirrors the existing `unsigned_override` pattern); a receipt outage
notice on both the inline HTML and ESC/POS print paths, derived from the
sale's own audit row; an operator Problem via the existing
`logging.Recent()` feed; and a background retry loop (mirrors
`setup_base_plugins.go`'s `basePluginRetryTick` idiom) that re-attempts
signing indefinitely and writes a `fiscal_signing_resolved` marker on
success.

New files: `internal/pages/fiscal_sign_hook.go` (+ test), two wasip1
test-fixture plugins under `internal/pages/testdata/`. Touches:
`pos_api.go`, `plugin_api.go` (new exclusivity check), `print_api.go`
(ESC/POS notice), `init.go` (wires the retry ticker),
`self_order_shop.go` (kiosk offline signal), `common/state.go`,
`receipt_test.go`, `internal/data/plugin_repo.go`,
`internal/plugins/manifest.go` (exclusivity enforced at install/update),
`web/ui/partials/receipt.html`, `web/locales/{en,ar,fa,tr}.json`,
`web/help/{en,ar,fa,tr}/sell.md`. Companion `ut-docs` commits publish
`reference/contracts/fiscal-sign-ask.md` (ADR-0039 instance) and a short
addition to `architecture/wasm-runtime.md`.

TDD throughout: every fix in this record was confirmed failing against
the pre-fix code (real assertion errors, not build failures) before being
confirmed passing — by Dev, and independently re-derived by Review.

## Independent review — round 1 (Opus, fresh context)

Found **4 blocker-class issues**, all money/compliance/data-integrity
class, none fixed at review time:

- **B1**: `fiscal.KeyTSEFailingSince` was stamped on any reachability
  failure (timeout, plugin-declared `unreachable`) — a direct ADR-0048
  Decision 1 violation ("TSE failing" must never be conflated with "TSE
  unreachable"). Concretely: an ISP outage on a healthy-LAN till could
  hard-block the *next* sale via the ADR-0048 gate, over a network blip —
  an offline-first regression on a product where checkout must never be
  blocked by the network. Also found the self-order kiosk tender path
  never threaded an offline signal into the dispatch at all, so a
  genuinely-offline kiosk burned the full 3s budget on every sale.
- **B2**: the exclusivity check (ADR-0041 Decision B: only one active
  `fiscal.sign.ask` answerer per till) ran only in the plugin-enable HTTP
  handler — installing or updating a plugin bypassed it entirely, so two
  active signers could coexist, with `EventBus.Ask`'s first-non-empty-wins
  semantics making it arbitrary (and silently unsigned, with no marker at
  all) which one actually signs.
- **B3**: the background retry tick aborted the entire queue on the first
  non-approved answer, so one permanently-confusing entry starved every
  sale behind it, indefinitely.
- **B4**: the signer payload reported gross tendered amount instead of
  net paid (didn't subtract `ChangeGiven`) — every other consumer in this
  codebase (`pos.netPayments`, `renderReceipt`) nets it. A €20 cash tender
  against a €12 sale would have reported €20 to an irreversible signed
  fiscal record.

Two smaller, related issues also flagged: the receipt outage notice never
cleared after a successful background retry, and the exclusivity check's
DB-error handling failed open rather than closed.

## Fix pass (Fable subagent, fresh context)

All four blockers plus the two smaller issues fixed:

- **B1**: removed `markTSEFailingSince` (and the retry tick's
  corresponding clear) entirely from `fiscal_sign_hook.go` — this
  mechanism's three response states can only ever observe a reachability
  problem, never a confirmed TSE fault, so per ADR-0048 Decision 1 it has
  no business writing that key at all; a future contract version adding a
  genuine "TSE confirmed broken" response state would be the right
  trigger. Kiosk offline wiring: new `#selforder-offline-flag` hidden
  input + `navigator.onLine` listener, threaded through
  `hx-include` into the checkout form and into `SaleInput.Offline`,
  mirroring the cashier path's existing signal.
- **B2**: moved the exclusivity check into `PersistManifest`
  (`internal/plugins/manifest.go`), alongside the existing
  `validatePageEntryKeys`/`validatePageEntryRoutes` collision checks,
  inside the install transaction — runs on install and update alike,
  excludes the plugin's own prior registration (safe self-update), rolls
  back cleanly on rejection. Enable-time check now fails closed on a DB
  error instead of silently permitting.
- **B3**: split the failure outcome into backend-level (evidence the
  shared backend itself is unreachable) vs. entry-level (a per-sale
  protocol issue); the retry tick now only aborts early on the former.
- **B4**: `buildFiscalSignPayload` now nets `ChangeGiven` out of each
  payment via `money.Money` subtraction, matching `netPayments`/
  `renderReceipt` exactly.
- Receipt notice now checks for a later `fiscal_signing_resolved` row on
  both render paths before showing the outage line.
- O(n²) enqueue cost during a long outage documented as an accepted,
  deliberate gap (bounded — backlogs are expected to stay small) rather
  than silently left unaddressed.

## Independent re-review — round 2 (Opus, fresh context, scoped)

Earned per the pipeline's own rule (a second round requires the first
finding a blocker-class issue) and scoped to the fix commits, not a full
re-review. **Verdict: B1, B2, B4 and both smaller fixes CONFIRMED FIXED**
(re-derived independently — read the actual diff, re-ran/rebroke several
tests personally, verified claims like "rollback.go never re-persists
hooks" and "the fail-closed fix genuinely refuses" by reading the code
rather than trusting the report). **B3 confirmed fixed as reported, with
one residual, explicitly non-blocking gap**: `askFiscalSign` classified
*any* `bus.Ask` error — including a plugin handler error well within
budget, e.g. a wasm guest trap on one specific payload — as
`fiscalSignFailedBackend`, so a deterministically-misbehaving plugin could
still starve the retry queue via a path B3's own fix didn't close. Two
minor doc-accuracy notes: ADR-0048's forward reference to "#675's future
failure callback" needed a dated update now that #675 landed without
wiring that key; and the kiosk's `offline` flag, being on an auth-exempt
route, is client-forgeable (bounded — never lets a sale through silently
unsigned, same trust posture the existing cashier flag already carries).

## Third pass — the residual finding, fixed directly (this session, not a
further subagent round)

The residual B3 gap and the two doc notes were small, mechanical, and
well-understood after two full review rounds — fixed directly rather than
spinning up a third Dev/Review cycle for a non-blocker:

- `askFiscalSign` now distinguishes a genuine budget timeout
  (`context.DeadlineExceeded`) or a declared `unreachable` (backend-level)
  from any other handler error (entry-level) — the latter no longer
  aborts the retry tick early. New test
  `TestFiscalSignRetry_HandlerErrorDoesNotStarveQueue` confirmed failing
  against the pre-fix classification (a forced `if true` override
  reproducing the exact bug) before being confirmed passing; existing
  `TestFiscalSignAsk_TimeoutDeclares` (a genuine timeout, still
  backend-level) re-verified unaffected.
- ADR-0048 gained a dated (2026-08-15) note recording that #675 landed
  without wiring `fiscal.tse_failing_since`, and why.
- The contract doc's failure-surface section was sharpened to describe
  the refined backend-vs-entry classification, and gained a note on the
  kiosk flag's bounded trust model.

## Verification (run personally, both before and after every fix pass —
not taken on either subagent's report)

- `go build ./... && go vet ./...` clean at every stage.
- Full `go test ./...` green at every stage (not just the touched
  package) — every package `ok`, no skips.
- All 7 CI guards green: `guard-data-access`, `guard-i18n` (1051 keys,
  all locales in parity), `guard-kiosk-engine`, `guard-plugin-menu-read`,
  `guard-compliance-claims`, `guard-help-topics`, `guard-docs-shots`
  (regenerated via `make docs-shots` after the classification fix
  touched `internal/pages/*.go` again).
- Real TDD, re-derived personally at each stage: forced
  `dispatchFiscalSignAsk` to always report no-signer and confirmed
  `TestFiscalSignAsk_UnreachableDeclaredProceedsAndDeclares` +
  `TestFiscalSignAsk_KnownOfflineShortCircuits` both failed with genuine
  assertion errors before the first commit; forced the classification
  fix's `context.DeadlineExceeded` check to always report backend-level
  and confirmed `TestFiscalSignRetry_HandlerErrorDoesNotStarveQueue`
  failed with the exact "must not starve entry 2" message before the
  final commit. Every temporary break was restored and re-verified
  (`git status` clean, suite green) before moving on.
- Locale key parity for `receipt.fiscal.unsigned_signing` confirmed
  across en/ar/fa/tr (same line number, all present); ar/fa/tr
  translations are the implementing agents' own best-effort styling (the
  NAS Ollama translation endpoint was unreachable from this session) —
  flagged for a follow-up translation pass, not presented as verified
  native translation.
- Visual verification of the receipt template change was structural, not
  a fresh screenshot: the new block reuses the exact `receipt-legal`/
  `receipt-legal-line` CSS classes the existing, already-shipped
  `unsigned_override` block uses immediately above it, so there is no new
  styling surface to regress. Stated plainly per the tester skill's
  requirement rather than implied.

## Deferred / accepted, not fixed this round

- **Orphan signature risk** (flagged as "risky, not certain" in round 1,
  not re-raised as a blocker): `pos.CompleteSale`'s own validation
  (line-discount bounds, payment coverage, negative-stock gate) can still
  fail *after* a successful `fiscal.sign.ask` dispatch, leaving a signed
  TSE record for a sale that never persists. Genuine gap, low-probability
  (`completeTender`'s earlier authorize loop already covers the most
  likely rejection reasons), not closed in this pass — worth a follow-up
  card if it proves live.
- O(n²) retry-queue enqueue cost during a long outage — documented, not
  fixed (see contract changelog).
- `tax.rate.ask`'s own exclusivity is still unenforced (pre-existing gap,
  noted in code, not retrofitted here — out of scope for this card).
- ut-plugin-tax-de → ut-plugin-tax-fiskaly split: universaltill/ut-docs#757,
  blocked on this card, no fiskaly sandbox credentials available in this
  environment (confirmed by cloning `ut-plugin-tax-de` and reading its own
  README this cycle).

## Verdict

**Ready to merge.** Two independent review rounds (one full, one scoped)
plus a direct fix of the scoped round's single non-blocker residual
finding. No open blocker-class issues.
