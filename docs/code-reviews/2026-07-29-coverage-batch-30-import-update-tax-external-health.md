# Test coverage batch 30: import_page.go / update_api.go / tax_hook.go / external_api.go / health_api.go

2026-07-29

Five previously-untested `internal/pages` files, all 0%: `import_page.go`
(CSV catalog import/export, a plugin's `htmlEscape` helper), `update_api.go`
(self-update check/apply API), `tax_hook.go` (the plugin tax-rate-ask
seam), `external_api.go` (menu-plugin reverse proxy), `health_api.go`
(`/healthz`).

Implemented by an Opus-model agent while this session (Sonnet) continued
the coverage push — same cross-model-review flow as batches 25/27/29.

## No bug found — legitimate outcome, both flagged bug classes checked and ruled out

This batch was specifically briefed to check for the two recurring bug
classes from this push (batch 28's "upload handler doesn't `MkdirAll`",
batches 11/23's "cwd-relative path instead of `paths.Data(...)`") since
`import_page.go` does file import/export. Independently re-verified via
source grep (not just trusting the summary):
`grep -n "MkdirAll\|WriteFile\|os.Create" internal/pages/import_page.go`
shows the export-save path (`os.MkdirAll(dstDir, 0o755)` immediately
before `os.Create(dst)`, line 104-106) already does this correctly —
confirmed as an existing safe pattern, not a gap. `/api/import`'s commit
path never touches disk at all (streams straight into
`catimport.Parse`/the catalog repo).

## Independent verification (sonnet, different model from the Opus implementer)

- Read all five new test files in full (432 lines total). No false-pass
  patterns: `htmlEscape` is table-tested including the no-double-encoding
  case (`&amp;` → `&amp;amp;`, not re-escaping the entity it just
  produced); import commit is checked for full referential correctness
  (item created, barcode attached to the RIGHT item id, opening stock
  landed as an inventory row with the right quantity, category created
  and linked) and idempotency (re-committing the identical file creates
  no duplicates); export round-trips its own header; the external proxy
  is checked for a real proxied body + content-type from a live
  `httptest.Server` upstream, 404 for three distinct "not proxyable"
  cases (empty id, unregistered id, registered-but-routeless), and 502
  (not 500 or a panic) when the upstream is unreachable;
  `update_api.go`'s manager gate is checked with auth deliberately left
  ON (not `UT_AUTH=off`) specifically so it proves the gate blocks
  *before* `selfupdate.Apply` or any network call, rather than just
  testing the gate exists.
- `go build ./...`, a full `go clean -testcache && go test ./...` (whole
  repo), and both CI guards — all pass. (One stale compiler diagnostic
  about a duplicate `TestMinorToDecimal` surfaced mid-run — confirmed via
  `grep -rn "func TestMinorToDecimal" internal/pages/*_test.go` and a
  clean `go build` that only one declaration exists; the implementer had
  already removed its duplicate before reporting back, the diagnostic
  was just stale.)
- Coverage confirmed: `registerExternalProxy` 100%, `registerHealth`
  100%, `htmlEscape`/`minorToDecimal` 100%, `registerImport` 63.6%,
  `AskTaxRateBP` 30% (only the no-subscriber decline path — the
  has-a-plugin-subscriber path needs real plugin hook wiring, reasonably
  out of scope for this batch), `registerUpdateAPI` 22.9% (deliberately
  stops at the manager gate — the network `CheckNow` and real
  `selfupdate.Apply` paths aren't unit-test territory). `internal/pages`
  overall: 64.7% → 67.1%.

## Coverage added

- **`import_page.go`**: `htmlEscape` (full character-class table, including
  the no-double-encoding case); preview writes nothing to the DB; commit
  creates items + barcode + opening stock + category, and is idempotent
  on re-import; manager gate on `/import`, `/api/import`,
  `/api/catalog/export`; export CSV header round-trips against the
  importer's own column names.
- **`update_api.go`**: both `/api/update/apply` and `/api/update/check`
  refuse (403) before doing any work when there's no manager session —
  auth deliberately left ON for this test.
- **`tax_hook.go`**: `pluginTaxRateAsker` satisfies `pos.TaxRateAsker`
  (compile-time assertion); with no plugin subscribed to `tax.rate.ask`,
  `AskTaxRateBP` declines `(0, false)` so the POS engine falls back to
  the item's own tax code, consistent with core having no built-in
  country tax rules.
- **`external_api.go`**: a registered plugin's route proxies the upstream
  body and content-type; empty/unknown/routeless plugin ids all 404; an
  unreachable upstream surfaces 502, not a 500 or panic.
- **`health_api.go`**: `/healthz` returns 200 `ok`.

## Verification

`go build ./...`, `go clean -testcache && go test ./...` (whole repo),
`scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh` — all pass.
`internal/pages` coverage: 67.1%.
