# GET /api/inventory/low-stock JSON responses use the {data,error} envelope (ut-docs#323)

## What shipped

`GetLowStock` (`internal/pages/inventory_api.go`, `GET
/api/inventory/low-stock`) responded to `Accept: application/json`
requests with a bare `{"items": ..., "count": ...}` on success and a bare
`{"error": ...}` on failure, instead of the `{ "data": …, "error": null }`
envelope `universal-till/CLAUDE.md`'s "API, formats, i18n" section
mandates for every JSON API response — and that every sibling JSON
handler already in this package (`sync_sales.go`, `pairing_api.go`,
`sync_admin.go`, `catalog/handlers.go`, the local `writeJSON` closure in
`ai_api.go`, …) already follows. Found during an earlier independent
review of #277 (which only touched `LowStockItem`'s field casing, not
this handler's response shape).

Fix: both branches now wrap their payload —
`{"data": {"items": …, "count": …}, "error": null}` on success,
`{"data": null, "error": "<message>"}` on failure. The HTML/HTMX branch
(the only consumer this endpoint actually has today —
`web/ui/pages/inventory.html`'s `hx-get`, which does not send
`Accept: application/json`) is untouched.

## Tests

- `TestGetLowStock_JSONAndHTML` strengthened: decodes the JSON response
  and asserts on the envelope shape (`data.items`, `data.count`,
  `error == null`) instead of a loose `strings.Contains(..., "count")`
  check that the pre-fix bare shape would also have satisfied.
- New `TestGetLowStock_JSONError_UsesDataErrorEnvelope`: forces the query
  to fail (closed `*sql.DB`, matching this codebase's established
  deterministic-error-forcing pattern) and asserts on the **raw** JSON
  keys (`map[string]json.RawMessage`) that `"data"` is present and
  literally `null` — not merely absent, which decoding into a Go struct
  field alone would not distinguish (a missing key and an explicit
  `null` both decode as a nil field).
- Both confirmed test-first: failed against the pre-fix code with the
  exact bare-map output, passed after the fix.

## Independent review

Fresh-context Sonnet subagent (complexity:easy card — this pipeline's
routing keeps review at a genuinely separate instance rather than the
same reasoning re-checking its own work, even at the cheapest tier).

**Verdict: safe to merge.** No blocker or should-fix findings.

- Re-ran `go build ./...`, `go vet ./...`,
  `go test ./internal/pages/... -run TestGetLowStock -v` (all pass), and
  the full `internal/pages` package suite (green, 48.4s).
- Independently re-verified the TDD claim: reverted only the handler fix,
  confirmed both tests fail with the same specific messages reported by
  Dev, confirmed the error-branch test genuinely distinguishes "no
  `data` key" from "`data` key present and null" (the exact trap this
  task called out), restored the fix, confirmed green again and the diff
  byte-identical to before the revert exercise.
- Confirmed the HTML/HTMX branch is byte-identical to `origin/main` by
  direct diff inspection.
- Confirmed N/A, with the reasoning: file-write bug classes (grep found
  zero file I/O in this handler), money (`LowStockItem` has no monetary
  fields), i18n (no new user-facing string — `data`/`error` are wire-
  protocol keys, not UI copy), offline-first (local SQLite read, no
  network dependency either way), plugin signing (no plugin code
  touched), user manual (the only UI consumer never sends
  `Accept: application/json`, so no shop-owner-visible surface changed).
- No real client/shop name or secret-shaped literal in the diff (grepped
  the diff; test fixtures are the existing generic `itm1`/`Apple`/`ABC`
  seed data already used elsewhere in this file).
- **Nitpick, not actioned**: the success branch builds a nested nameless
  `map[string]any` rather than a small named struct — consistent with
  `discovery_api.go`/`pairing_api.go`/`sync_api.go`'s existing house
  style in this package, so left as-is rather than introducing a
  one-off convention change in a bugfix diff.

## Explicitly out of scope

The independent review confirmed (and this task's own BA/Architect pass
had already found while grepping for "other handlers with the same
gap," per the issue's own acceptance-criteria bullet) that three other
handlers in this same file —
`CreateStockReceipt`/`respondSuccess`, `CreateNegativeInventoryOverride`/
`respondOverrideSuccess`, `CreateReturn`/`respondReturnSuccess` — and
three in `shifts_api.go` (`respondShiftSuccess`/`respondCloseSuccess`/
`respondAdjustmentSuccess`) write their response DTOs directly (e.g.
`{"movement_id": ..., "success": true}`) without the `{data, error}`
envelope: the same bug class, on different endpoints. Deliberately not
touched here — ut-docs#323 scopes only `GetLowStock`, and folding six
more handlers into this diff would turn an easy, single-endpoint fix
into an unreviewable one. Filed as a new Backlog card
(universaltill/ut-docs#378) rather than silently left unnoted.

## Safe to merge

Yes. Feature branch `fix/323-low-stock-envelope`, merged via `merge`
(not squash/rebase, per this pipeline's standing merge-method rule).
