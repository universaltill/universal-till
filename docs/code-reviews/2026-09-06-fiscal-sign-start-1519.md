# fiscal.sign.start fire-and-forget dispatch (ADR-0077 D1/D2, ut-docs#1519)

## What shipped

The first implementation card follow-up to ADR-0077 ("Fiscal signing gets
a two-phase start/finish dispatch"): a new plugin extension point,
`fiscal.sign.start` (`internal/plugins/manifest.go`), fired at the same
three call sites as the existing `fiscal.sign.ask` ("finish") point —
`internal/pages/pos_api.go`'s `completeTender`, `internal/pages/refund_page.go`'s
`POST /api/refund`, `internal/pages/inventory_api.go`'s `CreateReturn` — but
**earlier**: immediately after the ADR-0048 hard gate has allowed the
sale/refund/return to proceed, before the payment authorize/refund loop.

- Dispatch runs on a background goroutine (tracked via `common.Deps.AsyncWork`,
  same pattern as `printReceiptAsync`/`kitchen_print.go`) that calls
  `EventBus.Ask` — not `Publish` — so it can capture a
  `{"status":"acknowledged","tx_id":...,"tx_revision":...}` answer without
  the tender/refund/return path ever waiting on it.
- A captured `tx_id`/`tx_revision` is persisted (new table `fiscal_sign_starts`,
  migration `internal/db/migrations/005_fiscal_sign_starts.sql`, repo
  methods in `internal/data/fiscal_repo.go`) and echoed into the LATER
  `fiscal.sign.ask` request as two new optional fields,
  `started_tx_id`/`started_tx_revision`.
- `SaleID` minting moved so it happens when EITHER `fiscal.sign.start` OR
  `fiscal.sign.ask` has a subscriber (previously only the latter).
- Known-offline short-circuit extended to this new dispatch point, at all
  three call sites (via small request-local `pos.SaleInput` "carrier"
  structs at the refund/return sites, whose minted `SaleID` is threaded
  into the real `SaleInput` built later so both dispatches share an id).
- Deliberately NOT done here, per ADR-0077's own explicit sequencing:
  extending `validateExclusiveHookOwnership`'s exclusivity check to cover
  `fiscal.sign.start` (and `fiscal.sign.reconcile.ask`, not built at all
  yet) — bundled into the follow-up card, ut-docs#1520.
- Docs: `ut-docs/reference/contracts/fiscal-sign-ask.md` bumped to 1.7.0
  (new `fiscal.sign.start` section, new request-field subsection) and
  `ut-docs/architecture/wasm-runtime.md` updated — both committed directly
  to `ut-docs` `main` (`9bbbc35`), which is this pipeline's established
  precedent for doc-only prose changes to that repo (no CI path there is
  scoped to `reference/contracts/**`/`architecture/**`).

## Independent review

Opus subagent (card labelled `complexity:hard`), isolated worktree
(`.claude/worktrees/agent-a3428c5127a1e366d`, branched from a
`WIP: pre-review snapshot` commit on this branch). **Process note**: this
card's Dev phase was built inline by the orchestrating Sonnet session
rather than delegated to a Fable subagent — the change was fully scoped by
the already-accepted ADR-0077 (design questions already resolved by the
ADR's own independent-review revision) and didn't need additional
architectural judgment beyond applying that design, so the model-routing
table's "hard → Fable builds" was judged not to add value here; the
"hard → Opus reviews" half was followed as prescribed, which is the half
that actually gates quality.

Ran independently in its own worktree: `go build ./...`, `go vet ./...`,
`gofmt -l .`, `golangci-lint run ./...` (0 issues), `guard-data-access.sh`,
`guard-migration-version-collision.sh`, and the real test suite with
`-race -count=1` across `internal/pages`, `internal/plugins`,
`internal/data`, `internal/db` (all green at `-timeout 40m`; the exact
10-minute default timeout the review was first asked to use isn't enough
for this environment across those four packages together — cumulative
slowness pre-existing in this environment, not caused by this diff:
`internal/plugins` alone took ~594s untouched by this change).

Independently re-verified the TDD claim, not just trusted: temporarily
neutralized the `GetFiscalSignStart` → `payload.StartedTxID`/
`StartedTxRevision` block in `dispatchFiscalSignAsk`, confirmed
`TestFiscalSignStart_CapturesTxIDAndEchoesOnAskFinish` fails with the
expected assertion mismatch, restored the fix, confirmed all five
`TestFiscalSignStart_*` tests pass again.

### Findings — triaged

**Fixed before merge:**

1. **(Should-fix, near-blocker) Zero test coverage of the SaleID-carrier
   threading at all three call sites** — the original test set exercised
   `dispatchFiscalSignStart`/`dispatchFiscalSignAsk` directly but nothing
   drove the real HTTP handlers end to end, so a dropped
   `fiscalStartCarrier.SaleID` thread (silently falling back to
   `CompleteSale` minting its own id — permanent decorrelation of start
   from finish on a compliance path) would have gone unnoticed by every
   existing test. Added three new tests —
   `TestFiscalSignStart_SharesSaleIDWithFinishThroughTender`,
   `TestRefundFiscalSignStart_SharesSaleIDWithFinish`,
   `TestCreateReturn_FiscalSignStart_SharesSaleIDWithFinish` — each posting
   through the real handler, subscribing both events, and asserting the
   persisted sale's id matches the `fiscal_sign_starts` row's `sale_id`.
   Personally reverted the refund site's threading line and confirmed
   `TestRefundFiscalSignStart_SharesSaleIDWithFinish` fails with
   `sql: no rows in result set`, then restored it and confirmed green —
   this finding is real and now falsifiable. (Note: these three tests do
   NOT assert `started_tx_id`/`started_tx_revision` on the captured
   `fiscal.sign.ask` payload — within one real HTTP request the start
   goroutine and the finish dispatch run back to back with nothing
   waiting on the goroutine in between, so an in-process test handler
   with no real network latency can race either order; that echo is
   already proven, deterministically, by the isolated
   `TestFiscalSignStart_CapturesTxIDAndEchoesOnAskFinish`, which
   explicitly awaits the goroutine before asking "finish".)
2. **Refund dispatch fired too early** (`internal/pages/refund_page.go`):
   originally placed right after the ADR-0048 gate, ~15 refusal points
   (five DB-read 500s, three operator-input 400s) ahead of the refund
   actually being attempted — a cashier fat-fingering a return quantity
   would still start a TSE transaction abandoned forever. Moved to
   immediately before the `payment.<key>.refund` webhook, after every
   earlier check capable of refusing the refund outright — matching
   `completeTender`'s and `CreateReturn`'s own placement (both already
   correct: `CreateReturn` has no payment leg and all its validation runs
   before the gate; `completeTender`'s only remaining refusal after the
   gate is the authorize loop itself, which this point is deliberately
   ordered ahead of).
3. **Unvalidated plugin response** (`internal/pages/fiscal_sign_hook.go`):
   only `status == "acknowledged" && tx_id != ""` was checked; `tx_id` had
   no length cap and `tx_revision` no range check, so a misbehaving/hostile
   plugin's oversized or negative values would have been persisted
   verbatim and echoed into every later `fiscal.sign.ask` request.
   Added a 256-byte cap on `tx_id` and rejects `tx_revision < 0`
   (CLAUDE.md: "Validate all external input").
4. **Silently swallowed read error**: `dispatchFiscalSignAsk`'s
   `GetFiscalSignStart` lookup discarded a genuine DB read failure with no
   log — now `logging.L().Warnf`s it.
5. **Unnecessary SELECT on every sale for the entire installed base**: the
   echo lookup ran unconditionally, so a till running only today's shipped
   `fiscal.sign.ask`-only signer (which is every real signer until
   ut-docs#1521 lands) paid one extra primary-key SELECT per sale forever,
   for a row that provably never exists. Now guarded on
   `bus.HasSubscribers(fiscalSignStartEvent)`.
6. **Log level mismatch with the stated contract**: the start-side
   persist-failure log used `Warnf` (feeds the operator-visible Problems
   ring), but this point's own contract explicitly states "no operator
   alert" on any failure — a failed best-effort persist here isn't
   operator-actionable, unlike `declareUnsignedFiscalSale`'s `Warnf` which
   backs a real receipt/journal gap. Changed to `Infof`.
7. **Misleading doc comment on the async safety ceiling**: claimed
   `fiscalSignStartAsyncTimeout` (15s) "stops a wedged or slow-network
   signer" from leaking the goroutine — false for a Go handler, since
   `EventBus.Ask` calls it synchronously without itself selecting on
   `ctx.Done()`; for wasm it's moot for the opposite reason
   (`WasmRuntime.HandleEvent`'s own tighter `netTimeout`, enforced via
   wazero's `WithCloseOnContextDone`, always fires first). Corrected to
   describe what the timeout actually bounds.
8. **Unbounded/unredacted logging of the first non-suffix `EventBus.Ask`
   hook's answer**: `wasmResultLogLine` (`internal/plugins/wasm_runtime.go`)
   only routed `.ask`/`.authorize`/`.refund`-suffixed events through
   `safeAskResultForLog`'s size/redaction discipline; `fiscal.sign.start`
   deliberately doesn't match that suffix (the whole point of the ADR's
   compatibility choice), so its answer would have logged unbounded/raw.
   Added an explicit name check alongside the suffix check.
9. Two stale prose passages in `ut-docs/reference/contracts/fiscal-sign-ask.md`,
   found by the reviewer while reading the doc this diff itself amends —
   see the `ut-docs` commit message (`9bbbc35`) for detail; both prose-only,
   no wire/behavior change.

**Verified sound, not fixed (findings that don't hold up as real defects,
or are correctly out of this card's scope per the ADR's own sequencing):**

- The `EventBus.Ask`-not-`Publish` design decision itself — confirmed
  correct against `internal/plugins/ipc.go`'s actual dispatch-mode
  branching and `wasm_runtime.go`'s `Sync`.
- Goroutine lifecycle (`AsyncWork.Add`/`Done`, no lock held across the
  handler call, `-race` clean) — matches `printReceiptAsync` exactly.
- Carrier-struct safety at the refund/return call sites — no client-settable
  `sale_id`, no collision risk, `Offline` correctly mirrored.
- Migration 005 genuinely free of collision; `ui_smoke_test.go`'s hand-copied
  fixture verified column-identical to the real migration.
- The exclusivity-enforcement gap (`fiscal.sign.start` not yet folded into
  `validateExclusiveHookOwnership`) is real but explicitly, deliberately
  sequenced by ADR-0077 into ut-docs#1520 (bundled with
  `fiscal.sign.reconcile.ask`'s own implementation) — pulling it forward
  piecemeal here, without also building reconcile.ask, would contradict
  the ADR's own "not separable from this card" framing for #1520. Left
  alone, per document-first discipline; flagged here so #1520 doesn't
  lose track of it.
- `int64` `omitempty` asymmetry on `started_tx_revision` (a genuine
  `tx_revision: 0` alongside a present `tx_id` would omit the revision
  field) — theoretical only: fiskaly's own revision numbering starts at 1
  and never sends 0 for a real acknowledgment, so this isn't a reachable
  wire shape in practice. Not changed (would otherwise regress the
  till-with-no-subscriber zero-shape-change guarantee, since a
  non-`omitempty` revision field would always be sent).

## Beyond automated tests

- Personally re-read all three dispatch call sites directly (not trusting
  doc comments) to confirm the "before the payment loop" ordering claim,
  and fixed the one site (refund) where the code didn't yet match the
  claim.
- Traced the carrier-struct SaleID threading by hand at both non-tender
  call sites to rule out a cross-sale-id collision, before also proving it
  with the new tests above.
- Confirmed via `git log`/comment cross-reference that the ADR's own
  Consequences section genuinely assigns the exclusivity-group fix to
  ut-docs#1520, not this card — this card's manifest.go change is
  additive-only (a new constant, no exclusivity change), matching that
  sequencing exactly.

## Card status — NOT fully "done" per its own acceptance criteria

ut-docs#1519's acceptance criteria require fiskaly's answer on the
transaction-cancellation question (an unfinished `start` with no core-side
cancel/abort path) to be either recorded, or explicitly accepted as an open
risk. The vendor has not answered as of this merge — the shipped contract
doc's own "Open item" subsection records this honestly as an accepted,
unresolved risk (matching the ADR's own "accepted as an open risk, not a
solved one" framing), which satisfies the card's own "recorded answer, OR
an explicit accepted-risk note" acceptance criterion, but the underlying
question is genuinely still open. Do not close this as fully resolved —
the go-live gate (ADR-0077 D5, ADR-0044 Decision 5) still needs it before
this reaches a live German till.

## Verdict

**Safe to merge.** Build/vet/gofmt/lint clean; full `-race` suite green
(`internal/pages`, `internal/plugins`, `internal/data`, `internal/db`);
all CI-blocking guards pass; the TDD claim behind the core round-trip test
was independently falsified and restored, twice (once by the reviewer, once
by this session against the finding-2 SaleID-threading fix). Nine review
findings fixed; the two left open (exclusivity sequencing, the
`omitempty` asymmetry) are deliberate/theoretical, not defects.
