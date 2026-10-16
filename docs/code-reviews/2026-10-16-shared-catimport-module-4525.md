# Review — shared catimport Go module (ut-docs#4525)

- **Date:** 2026-10-16 · **Lane:** lane:local · **Built by:** Opus 5.5 · **Reviewed by:** Fable 5.1 (independent subagent)
- **Design:** ADR-0164 card 1 (universaltill/ut-docs#4524, Proposed).
- **Scope:**
  - The plain parsers (generic CSV/XLSX, Shopify, SumUp, Square, Loyverse, ERP) move into the module `github.com/universaltill/universal-till/catimport` (its own `go.mod`; the root `replace` points at it). The barcode registry moves to `catimport/barcode`, with embedded prices now `int64` minor units. The item-type and net-quantity rules move to `catimport/catalogrules`.
  - ADR-0051 `Row` / `ToRows` / `RowFor`, with an optional `barcode_type`.
  - Per-format external ids.
  - Guards `guard-catimport-module.sh` and `check-catimport-pin.sh`, plus the CI steps.
  - `internal/catsync` stores a typed barcode as-is.
  - `.bkp`, vouchers, icons and placeholders stay in `internal/catimport`.

## Verified
- **No behaviour change:** 172 CSV inputs × settings gave 6,373 parser outputs, byte-identical before and after the move (apart from the new id field). Test counts per moved file are identical.
- **Builds and tests:** module + root `go build/vet/test` and golangci-lint v2.14.0 (0 issues). The only root failure is the environment-only `mobile` `TestStart_ListensOnAllInterfaces…` (a VPN interface on this Mac), which also fails on `main`.
- **Guards:** `guard-catimport-module`, `guard-data-access`, `guard-core-neutral` (scanning `catimport/`), the docs-shots surface guard and the dead-code baseline all pass.
- **Pin check:** `check-catimport-pin.sh` was exercised in a scratch clone with real tags.
- **Tag flow:** `release.yml`, nightly and iOS all match `v*`, which never matches `catimport/v…`.
- **Playwright:** catalogue-import, item-number barcode, SKU, barcode-scan and voucher-scan specs, 32 tests pass.
- **Consumer:** a scratch consumer module with a `replace` tidies, builds and runs; ut-cloud's build passes with it.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | Medium | A Loyverse external id changed when a sibling variant was added in a later export, breaking ADR-0164 re-import idempotency | Fixed: always `handle/SKU` when the row has a SKU. A test proves the id is stable when a variant is added, and it fails against the old rule |
| M2 | Medium | `Row.Barcode` was the decoded lookup key with no symbology, so consumers double-decoded (#936) | Fixed: optional `barcode_type`. catsync stores a typed barcode only if its type is enabled and the code is that type's key. An end-to-end test with a price-embedded EAN-13 failed before the fix with the real error |
| L1 | Low | ToRows drops description, age restriction, net quantity, takeaway VAT and image | ut-docs#4542 (extend ADR-0051 rows) |
| L2 | Low | The README described ut-cloud's pin check as existing | Reworded as "will run" (#4528); the checkout needs `fetch-depth: 0` + tags |
| L3 | Low | Root `go.mod` require order wasn't tidy-stable | Sorted |
| L4 | Low | The docs-shots surface hash bump needs the trailer | Commit carries `Docs-Shots-Unchanged: true` (`internal/pages` changes are import paths and comments only) |
| L5 | Low | Stale comment in ut-cloud `internal/claims/directives.go:377` (`internal/barcode`) | Fix on the next ut-cloud touch (#4528) |

## ADR-0164 deviations (to fix in its text while Proposed)
- `placeholder.go` stays in the till: it maps onto the till's bundled icon files.
- The barcode registry moved whole into the module; embedded price is in minor units.
- Loyverse ids are `handle/SKU`, and `Row` gains `barcode_type`.

**Verdict:** safe to merge for the till. ut-cloud consumes it from tag `catimport/v0.1.0` (cut after merge).
