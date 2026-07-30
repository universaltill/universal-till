# Code review — coverage batch 6: `internal/pages/catalog` (2026-07-30)

**Branch:** `coverage/pages-catalog` · **Scope:** test-coverage push batch 6
(`ut-docs/QUEUE.md`, "Test-coverage push, remainder").
**Coverage:** 53.3% → **98.0%** of statements.
**Reviewer:** independent different-model review (opus subagent), author =
pipeline dev (fable). **Verdict: SAFE TO MERGE — no blocking findings, no
should-fix findings.**

## What changed

- 4 new test files: `cost_currency_test.go`, `lookup_endpoints_test.go`,
  `handlers_coverage_test.go`, `handlers_errors_test.go` — httptest through
  the real mux, real minimal-schema sqlite per test, DB-state assertions
  after every mutation (not status-code-only).
- Production changes, deliberately minimal:
  - **Real bug fix (medium), TDD-first** — see below.
  - A test seam: `var newLookupClient = func() *productlookup.Client`
    replacing the inline construction in `Register`, so tests point the
    barcode-lookup endpoints at hermetic httptest sources. Production
    behavior identical; every test override restores via `t.Cleanup`
    (reviewer grep-verified no leak).
  - Removed the then-unused `fmt` import.
  - `web/ui/partials/catalog_variants.html`: cost input `step`/`placeholder`
    made currency-decimals-aware, matching its sibling price fields.

## The real bug (medium): item cost-price ignored 0-decimal currencies

`POST /api/catalog/item-cost` converted major→minor with a hardcoded
`*100`, the variants panel rendered minor→major with a hardcoded
`/100` + `%.2f`, and the template hardcoded `step="0.01"` — all wrong for
0-decimal currencies (IRR/IRT/IQD/AFN/JPY, all in `httpx.currencies`;
Iran is an explicit target market). A rial shop entering a 650,000-rial
cost stored **65,000,000** — margin report 100× wrong — and a stored cost
rendered back 100× too small. The modifier-option handler in the same
file had already fixed exactly this class (its comment spells out the
reasoning); the cost field predated that fix.

**TDD arc, proven red first** (author, then re-proven by the reviewer via
isolated revert of only the fix hunks):
- `TestItemCost_RespectsZeroDecimalCurrency_OnSave` → red:
  `want cost_price 650000, got 65000000`.
- `TestItemCost_RespectsZeroDecimalCurrency_OnDisplay` → red: panel showed
  `value="6500.00"` and `step="0.01"`.
- `TestItemCost_TwoDecimalCurrencyRoundTrip` (GBP control) — green in BOTH
  states (reviewer-confirmed), i.e. it guards the common path rather than
  merely tracking the bug.

Fix mirrors the established pattern:
`httpx.CurrencyByCode(d.CurrentState().Currency).Decimals` with
`math.Pow(10, decimals)` both directions; display via
`strconv.FormatFloat(..., 'f', decimals, 64)`.

## Hermeticity — proven, not claimed

- Whole package suite passes with `HTTP_PROXY`/`HTTPS_PROXY` poisoned to
  `http://127.0.0.1:9` and under `-race` (author and reviewer both ran it).
- Barcode-lookup tests use httptest sources through the new seam;
  lookup-image tests exercise the **real** `FetchImage` SSRF allowlist via
  a stub `http.RoundTripper` for `https://images.openfoodfacts.org` — the
  allowlist check runs before the transport is consulted, and the
  disallowed-host case asserts the transport is never reached.
- The lookup endpoint's audit trail is asserted for real (hit + miss rows
  in `audit_log`, `found` flag correct) — the handler swallows audit
  errors, so the test schema grows the table locally to observe them.

## False-pass audit

- Author probes (4/4 caught): variant-wins-over-item clearing removed in
  the barcode handler; `IsActive` filter dropped from variant-options;
  audit `found` hardcoded true; `saveLookupImage` call skipped.
- Reviewer's own fresh probes (4/4 caught): `formCheckboxActive` forced
  true; create-handler barcode-attach error swallowed; `hx-swap-oob`
  injection dropped; variant handler's multi-value `isActive` scan reduced
  to first-value-only.

## Honestly-untestable remainder (documented, not faked)

- `os.Create`/`png.Encode` error branches on just-created files/dirs in
  both image-upload handlers and `saveLookupImage` — would require
  faulting a path `MkdirAll` just succeeded on; not reachable hermetically
  (the *MkdirAll* failures themselves ARE covered, via a file squatting on
  the directory path).
- `bufResponseWriter.WriteHeader` reads 0.0% because it is an empty-body
  no-op (zero statements — cover display artifact); the contract test does
  call it.

## Gate

`go build ./...` ✓ · `go vet ./...` ✓ · full `go test ./...` exit 0 ✓ ·
all 5 `scripts/ci/guard-*.sh` ✓ · Playwright e2e 20/20 ✓ (author ran the
full suite; reviewer verified harness enumeration — 20 tests in 9 files —
and judged a re-run unwarranted for a backend-scoped change, stated
honestly). No leftover test servers (checked ports/process list).

## Resumed-session re-verification (2026-07-30, pre-commit)

The pipeline session that produced this batch (and the review above) died
before commit. The resuming session did **not** take the above on faith:

- **Second independent different-model review (opus subagent), run fresh**:
  SAFE TO MERGE, zero blocking / zero should-fix. It re-ran build/vet, the
  package suite (98.0% confirmed), hermeticity (poisoned proxy + `-race`,
  3.4s, clean), `guard-data-access` + `guard-i18n`, and 3 mutation probes of
  its own (save-path `*100` re-hardcode, display-path `/100` re-hardcode,
  audit `found` forced true) — all 3 caught by DB/behavior assertions, tree
  restored byte-identical. Its nits: the `InitCurrency` cleanup restores a
  hardcoded `"GBP"` rather than the prior value (safe today — serial tests,
  GBP default; noted for the future), and the "Coca-Cola"/`5449000000996`
  fixture is the canonical Open Food Facts sample, not a client name.
- **TDD arc re-proven a second time** by the resuming session itself, in an
  isolated tree copy: fix hunks reverted → `OnSave` red (`want cost_price
  650000, got 65000000`), `OnDisplay` red (`value="6500.00"`,
  `step="0.01"`), GBP control green in the broken state; restored → all
  green.
- Full gate re-run at commit time: `go build ./...` ✓ · `go vet ./...` ✓ ·
  full `go test ./...` exit 0 ✓ · all 5 guards ✓.

## Reviewer nitpicks (recorded, no action)

- Cost value formats from `d.CurrentState().Currency` while the template
  `step` reads the process-global `ActiveCurrency` — a pre-existing
  dual-source pattern shared by every sibling field; production keeps the
  two in sync via `httpx.InitCurrency`.
- Cost threads as raw `int64` minor units at the DB boundary rather than
  `money.Money`, matching the existing modifier-option handler —
  pre-existing, out of scope for a coverage batch.
