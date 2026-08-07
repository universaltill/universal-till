# Six more inventory/shift JSON handlers use the {data,error} envelope (ut-docs#378)

## What shipped

Following ut-docs#323 (`GetLowStock`), six more JSON response paths still
wrote bare response structs instead of the `{ "data": …, "error": null }`
envelope `universal-till/CLAUDE.md`'s "API, formats, i18n" section
mandates:

- `internal/pages/inventory_api.go`: `respondError`/`respondSuccess`
  (`CreateStockReceipt`), `respondOverrideError`/`respondOverrideSuccess`
  (`CreateNegativeInventoryOverride`), `respondReturnError`/
  `respondReturnSuccess` (`CreateReturn`).
- `internal/pages/shifts_api.go`: `respondShiftError`/`respondShiftSuccess`
  (`OpenShift`), `respondCloseError`/`respondCloseSuccess` (`CloseShift`),
  `respondAdjustmentError`/`respondAdjustmentSuccess`
  (`RecordCashAdjustment`).

Additionally fixed 3 more `writeJSON` call sites in `inventory_api.go`'s
invalid-JSON-body branches (`CreateStockReceipt`, `CreateNegativeInventoryOverride`,
`CreateReturn`) that wrote the same bare structs directly, bypassing the
named helpers above — same handlers, same bug class, found while touching
these exact lines (not new scope).

Fix: every success path now wraps its payload as
`map[string]any{"data": <response struct>, "error": nil}`; every error
path as `map[string]any{"data": nil, "error": <message string>}`. The
HTML/HTMX branches of all six handlers are byte-for-byte unchanged.

**Note for any external/manual API consumer**: on the error path, the
error text used to live on the response struct's `.message` field
(`json:"message,omitempty"`); it's now on the envelope's top-level
`"error"` key instead. `.message` is dead going forward on the error
branch (the field stays in the struct for the success shape, e.g.
`data.message` — unused today but harmless). No known consumer reads
`.message` — the only UI callers are `web/ui/pages/inventory.html` /
`shifts.html`, plain HTMX forms that never send
`Accept: application/json` and so only ever exercise the untouched HTML
branch.

## Tests

- `envelopeOf(t, body)` test helper added (`inventory_api_test.go`,
  shared across both test files in the same package): decodes into
  `map[string]json.RawMessage` so "data key present and null" is
  distinguishable from "data key absent entirely" — the exact trap a
  struct-field decode can't catch, same pattern #323 established.
- All six existing success-path tests
  (`TestCreateStockReceipt_JSONAndHTML`, `TestCreateReturn_ByOriginalSaleID`,
  `TestOpenShift_JSONAndValidation`, `TestCloseShift_ComputesExpectedCashAndVariance`,
  `TestRecordCashAdjustment`) strengthened from a loose
  `strings.Contains(..., "success":true)` check to decoding and asserting
  on the envelope shape.
- Error-path envelope assertions added to `TestCreateStockReceipt_InvalidJSON`,
  `TestCreateReturn_ValidationErrors`, `TestOpenShift_JSONAndValidation`
  (both the failed-second-open and invalid-JSON cases),
  `TestCloseShift_ComputesExpectedCashAndVariance`'s already-closed case,
  and a new JSON-Accept case added to `TestRecordCashAdjustment_ValidationErrors`.
- New `TestCreateNegativeInventoryOverride_JSONEnvelope`: the override
  handler had no existing JSON-Accept test at all (its other tests all
  exercise the HTML/HTMX form path) — added success + error coverage.
- TDD claim re-verified personally: reverted `inventory_api.go` and
  `shifts_api.go` back to the pre-fix bare-struct writes (`git stash`),
  ran the full targeted test list — 10 of the updated/new tests failed
  with the expected "not a {data,error} envelope" diagnostics, the rest
  (validation/auth tests unrelated to response shape) still passed as
  expected. Restored the fix, confirmed all green again.

## Independent review

Fresh-context Sonnet subagent (complexity:easy card).

**Verdict: SAFE TO MERGE.** No blockers.

- Independently grepped every remaining `writeJSON` call site in both
  files and confirmed no stragglers were missed, including the 3
  invalid-JSON branches.
- Independently mutation-tested the fix (reverted `respondSuccess`/
  `respondError` in a throwaway copy, confirmed the new test fails with a
  clear diagnostic naming the bare shape it caught).
- Confirmed HTML/HTMX branches byte-for-byte unchanged, and confirmed via
  the two production templates that neither sends
  `Accept: application/json`, so no shop-owner-visible behavior changed.
- Confirmed no money/i18n/data-access rule implicated (no SQL, no
  `money.Money`, no imports changed — diff is struct-literal → map
  literal plus tests/comments).
- Confirmed the plain-string `"error"` value matches this package's
  existing convention (`pairing_api.go`, `sync_admin.go`, `sync_api.go`,
  `api_gates.go`, `discovery_api.go` all use bare string errors already)
  — no new inconsistency introduced.
- Re-ran `go build ./...`, `go vet ./internal/pages/...`, the full
  targeted test list, the full `internal/pages` suite, and
  `scripts/ci/guard-data-access.sh` — all clean.
- **Nit, fixed**: a leftover duplicate doc-comment line above
  `respondReturnError` (two comment lines saying the same thing) —
  cleaned up before commit.
- **Nit, noted above, not actioned as code**: the `.message` field
  going dead on the error path is a wire-format-visible change for any
  hypothetical external consumer — called out in "What shipped" above
  rather than silently left for someone to discover.

## Broader sweep (acceptance criterion)

Grepped `internal/pages/**` for further handlers with the same gap, per
ut-docs#378's own acceptance criteria (not folded into this diff, to keep
it reviewable): `pos_api.go` (`POST /api/pos/tender`'s JSON branch),
`marketplace_v1_stub.go` (5 endpoints), `plugin_api.go` (7 handlers),
`plugins_store_page.go` (3 endpoints), `update_api.go`, `data_api.go` (4
endpoints + 2 bare GETs), `plugin_page.go` — all respond
`{"success":…}`/bare-map shaped, not the envelope. Filed as
universaltill/ut-docs#387 rather than silently expanding this card's scope.

## Safe to merge

Yes. Feature branch `fix/378-inventory-shifts-json-envelope`, merged via
`merge` (not squash/rebase, per this pipeline's standing merge-method
rule).
