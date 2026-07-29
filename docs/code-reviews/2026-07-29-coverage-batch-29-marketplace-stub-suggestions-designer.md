# Test coverage batch 29: marketplace_v1_stub.go / suggestions_api.go / designer_page.go

2026-07-29

Three previously-untested `internal/pages` files, all 0%:
`marketplace_v1_stub.go` (a dev-only marketplace echo server used for
local plugin-install testing without a real marketplace backend),
`suggestions_api.go` (basket cross-sell chip strip, driven by the
`related_items` co-occurrence table), `designer_page.go` (the shortcut-
button grid designer page).

Implemented by an Opus-model agent while this session (Sonnet) continued
the coverage push — same cross-model-review flow as batches 25/27.

## No bug found — legitimate outcome

All three files are read/render/echo handlers with no disk writes, so
neither of this push's two recurring bug classes (batch 28's "upload
handler doesn't `MkdirAll` its target dir", batches 11/23's "cwd-relative
path instead of `paths.Data(...)`") could apply here — confirmed directly
(`grep -n "os.WriteFile\|paths.Data\|MkdirAll"` on all three target files
returns nothing). Every route/branch behaved as documented once actually
exercised.

## Independent verification (sonnet, different model from the Opus implementer)

- Read all three new test files in full (426 lines total). No false-pass
  patterns: assertions check real HTTP status codes per branch (happy
  path, wrong method → 405, missing required field → 400, unknown field
  rejected via `DisallowUnknownFields`), real rendered body content (a
  seeded `related_items` co-occurrence edge actually produces a
  suggestion chip with the right name/SKU; an empty basket renders the
  strip `hidden` with zero chips, not just "200 OK"), and the
  `decodeJSONBody` nil-body/malformed-JSON branches driven directly
  rather than only through the HTTP layer.
- Confirmed the "no disk writes → the two known bug classes don't apply"
  reasoning myself via source grep, not just trusting the summary.
- `go build ./...`, a full `go clean -testcache && go test ./...` (whole
  repo), and both CI guards — all pass.
- Coverage confirmed: `registerDesigner` 100%, `registerMarketplaceV1Stub`
  90.3%, `decodeJSONBody`/`writeJSONResponse` 100%, `registerSuggestions`
  100%. `internal/pages` overall: 63.2% → 64.7%.

## Coverage added

- **`marketplace_v1_stub.go`**: gating (nil `Deps`/nil `Cfg` register no
  routes and don't panic; disabled when neither `DevMode` nor
  `UT_ENABLE_MARKETPLACE_STUB=true`; enabled via either); every route's
  happy path (`/v1/install/intents`, `/v1/install/status`,
  `/v1/install/bundles/export`, `/v1/install/bundles/import`,
  `/v1/telemetry/plugins`) plus their wrong-method 405 and
  missing-required-field 400 branches; unknown JSON fields rejected.
- **`suggestions_api.go`**: an empty basket renders a hidden strip with
  no chips; a basket item with a seeded `related_items` edge surfaces the
  related item as a visible, add-by-SKU chip.
- **`designer_page.go`**: the page renders; a seeded shortcut button
  (against a real item, satisfying the FK) renders its label through
  `ui.ToVM` in the grid.

## Verification

`go build ./...`, `go clean -testcache && go test ./...` (whole repo),
`scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh` — all pass.
`internal/pages` coverage: 64.7%.
