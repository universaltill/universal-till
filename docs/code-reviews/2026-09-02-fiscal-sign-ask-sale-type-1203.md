# Code review: fiscal.sign.ask payload gets a sale_type field (ut-docs#1203)

## What shipped

`ut-docs#1203`: `fiscalSignAskPayload` (`internal/pages/fiscal_sign_hook.go`)
carried no field distinguishing a refund/return from a sale — a €2.40
refund and a €2.40 sale produced byte-for-byte identical payloads (same
`total`, `payments`, `vat_breakdown`), so a DSFinV-K signer had no way to
record a return as a Rückgabe rather than positive turnover: an
irreversible wrong TSE record.

**Fix** (`complexity:hard` — cross-repo compliance contract, routed to
Fable for Dev per this pipeline's model-routing table):
- Added `SaleType string \`json:"sale_type"\`` to `fiscalSignAskPayload`
  (never `omitempty` — every sale has a type), populated from
  `pos.SaleInput.SaleType` in `buildFiscalSignPayload`. That field already
  existed on `SaleInput` and was already correctly set to `"return"` at
  both refund/return construction sites (`refund_page.go:339`,
  `inventory_api.go:520`) — no new plumbing needed upstream, just copying
  a value that was already there.
- Defensive empty-default (`if saleType == "" { saleType = "sale" }`)
  mirroring `pos.CompleteSale`'s own fallback (`internal/pos/sales.go:540-
  541`) — added after the orchestrator's own testing pass found that
  `buildFiscalSignPayload` runs *before* `CompleteSale` on the same
  `*SaleInput` (passed by value into `CompleteSale`, so its normalization
  never propagates back), so a future caller relying on `CompleteSale`'s
  default would otherwise ship an empty `sale_type` on the signed record.
  Unreachable in production today — `completeTender`'s two callers both
  set `SaleType: "sale"` explicitly — but real for any future caller
  (e.g. the eventual refund-dispatch wiring, universal-till#594).
- 3 new tests in `fiscal_sign_hook_test.go`:
  `TestFiscalSignPayload_SaleTypeMirrorsSaleInput` (unit-level, both
  values, asserts the marshaled wire JSON), `TestFiscalSignPayload_
  EmptySaleTypeDefaultsToSale` (the defensive-default case), and
  `TestFiscalSignAsk_ReturnDispatchCarriesSaleType` (dispatch-level:
  drives the real `dispatchFiscalSignAsk` + shared `EventBus` with a
  refund-shaped `SaleInput`, captures the real wire bytes off a spy
  subscriber, and asserts a `"sale"`/`"return"` pair differ in exactly one
  key).

**Companion doc change** (`ut-docs`, separate PR): `reference/contracts/
fiscal-sign-ask.md` bumped to 1.6.0 — JSON example, a new `### sale_type`
subsection, and a changelog row, following the exact structure of the
existing 1.2.0/1.3.0/1.5.0 entries. No ADR — this is a routine additive
contract-version bump under the existing ADR-0041/ADR-0044 extension-point
model, same as every prior additive version.

## Review

Independent review by an Opus subagent in an isolated git worktree
(`complexity:hard` routing — review stays Opus, deliberately not Fable, so
a different model looks at Fable's own work).

**Verdict: no bugs in the Go change.** Specifics the reviewer chased down:

- **Default semantics match `pos.CompleteSale`'s exactly**, and the local
  default is genuinely necessary (not redundant) given `CompleteSale`
  takes `SaleInput` by value.
- **No missed call site** — `fiscalSignAskPayload` is constructed in
  exactly one place; `dispatchFiscalSignAsk` has exactly one production
  caller (`pos_api.go:226`, via `completeTender`).
- **Reachability claim verified**: `completeTender`'s two callers
  (`pos_api.go:1177`, `self_order_shop.go:372`) both set `SaleType:
  "sale"` explicitly; the refund paths call `CompleteSale` directly and
  never dispatch `fiscal.sign.ask` at all today (a separate, already-
  tracked gap — universal-till#594 / ut-docs#999).
- **Backward-compat: genuinely additive** — new key, no `omitempty`, no
  existing field's semantics touched.
- **Cross-reference verified**: `plugins.SaleCompletedEvent.SaleType`
  really is `json:"sale_type"` with the same two values — the "mirrors
  the sibling event" claim in both the code comment and the doc is
  factually correct.

**One finding, fixed**: the companion `ut-docs` doc named
`ut-plugin-tax-fiskaly` as a second real consumer to update. That plugin
does not and will not exist — **ADR-0055** (Accepted) withdrew ADR-0044
Decision 3's signing-plugin split; TSE signing stays inside
`ut-plugin-tax-de`. The new prose contradicted an accepted ADR and the
same document's own "Known consumers" section three lines below it.
Fixed in the `ut-docs` branch (two occurrences) before merge — `ut-plugin-
tax-de` named as the sole consumer, with a one-line note pointing at
ADR-0055 for why no second plugin exists.

One nit left as-is (comment says "mirrors ... verbatim", which isn't
strictly true given the empty-default normalization — adequately
mitigated by the explicit fallback comment two lines below it) and one
pre-existing, out-of-scope doc inconsistency noted for a future card (the
contract doc's 1.5.0-era JSON example's `total` doesn't reconcile with its
own prose — a v1.5.0 regression, not touched by this diff).

## TDD verification (independently re-run twice — by the orchestrator, then again by the reviewer in isolation)

Orchestrator, on the `SaleType: in.SaleType` → dropped mutation:
```
$ go test ./internal/pages/... -run 'TestFiscalSignPayload_SaleTypeMirrorsSaleInput|TestFiscalSignAsk_ReturnDispatchCarriesSaleType' -count=1
--- FAIL: TestFiscalSignPayload_SaleTypeMirrorsSaleInput
    payload.SaleType = "", want "sale"
    payload.SaleType = "", want "return"
--- FAIL: TestFiscalSignAsk_ReturnDispatchCarriesSaleType
    sale dispatch: wire sale_type =  (present=true), want "sale"
    {"sale_id":"sale-type-probe","sale_type":"","currency":"EUR",...}
FAIL
$ # restored
$ go test ./internal/pages/... -run 'TestFiscalSignPayload_SaleTypeMirrorsSaleInput|TestFiscalSignAsk_ReturnDispatchCarriesSaleType' -count=1
ok
```

Orchestrator, on the empty-default (`TestFiscalSignPayload_
EmptySaleTypeDefaultsToSale`) written test-first:
```
$ go test ./internal/pages/... -run TestFiscalSignPayload_EmptySaleTypeDefaultsToSale -count=1
--- FAIL: payload.SaleType = "", want "sale" (CompleteSale's own default)
$ # fix applied
$ go test ./internal/pages/... -run TestFiscalSignPayload_EmptySaleTypeDefaultsToSale -count=1
ok
```

Reviewer, four independent mutations in its isolated worktree (bare revert
→ compile error; field kept, population dropped → all 3 new tests fail on
real assertions, dispatch test prints the actual captured wire JSON with
`sale_type:""`; mirror hardcoded to `"sale"` → the `return` subtest and
dispatch test fail; empty-default alone dropped → only that one test
fails) — confirms each test pins a genuinely distinct behavior and the
dispatch test is not a mock (asserts on bytes captured off the real shared
`EventBus`, not a stub).

## Full gate

```
gofmt -l .                                    → empty
go build ./...                                → clean
go vet ./...                                  → clean
go test ./internal/pages/...  (full package)  → ok, 153s (pages/catalog/common)
bash scripts/ci/guard-data-access.sh          → pass
bash scripts/ci/guard-i18n.sh                 → pass (no user-facing string added — wire-only field)
bash scripts/ci/guard-compliance-claims.sh    → pass (232 files, no forbidden claims — run by
                                                  the reviewer; not normally scoped to this diff
                                                  since it's Go-only, but relevant given the
                                                  companion doc's compliance-adjacent prose)
```

No i18n key added (JSON wire field, not user-facing), no SQL, no UI
surface touched — nothing for `guard-kiosk-engine.sh`/`guard-htmx-
loaded.sh`/etc. to check.

## Safe-to-merge verdict

**Yes**, both branches, once the `ut-plugin-tax-fiskaly` doc fix above
landed (done, before this record was written). Full gate green on both
repos, TDD claim independently re-verified twice (orchestrator + isolated
reviewer, four mutations total), no regressions in the full `internal/
pages` suite.

## Explicitly deferred (new Backlog cards, not built this cycle)

Per this card's own acceptance criteria, which explicitly allow the
consumer-plugin updates to be tracked separately:

- `ut-plugin-tax-de`: branch on `sale_type` once installed and signing a
  return, per the 1.6.0 contract addition.
- Rebuild/rebase `universal-till` PR #594 (draft, blocked on this card;
  its branch predates the 2026-08-31 `main` history-rewrite incident
  #1374/#1378 and is now too diverged for a normal rebase) against
  current `main` to pass `sale_type` through and take it out of draft —
  this is also the change that will make the empty-default fallback above
  reachable for the first time (refunds don't dispatch `fiscal.sign.ask`
  at all today; that PR is what wires the dispatch in).
- Pre-existing, unrelated to this diff: the contract doc's 1.5.0-era JSON
  example doesn't reconcile with its own prose (found by the reviewer,
  confirmed already present on `main` before this change).
