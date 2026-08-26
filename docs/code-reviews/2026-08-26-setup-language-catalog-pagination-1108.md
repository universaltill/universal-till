# Setup wizard language catalog: follow the full marketplace pagination (ut-docs#1108)

**Card:** universaltill/ut-docs#1108 — `setupLanguageCatalogEntries` (the
setup wizard's step 1 catalog fetch, `internal/pages/setup_language_catalog.go`)
only ever read page 1 of ut-cloud's `ListPlugins{Capability:["language"]}`
and ignored pagination entirely. Invisible with today's 2 published language
packs (they fit on ut-cloud's `defaultPageSize=20` first page); would
silently drop every listing past page one as more `ut-plugin-language-*`
packs ship, defeating the wizard's own "list every language the product can
run in" requirement with no error, no log a human would notice.

**Complexity:** medium/hard-adjacent (a real production wire-format bug, not
just a loop). Dev inline (Sonnet), review at Sonnet fresh-context subagent
per this repo's model-routing note, isolated worktree.

## What shipped

1. `setupLanguageCatalogEntries` now loops calling `client.ListPlugins` with
   `PageToken`, appending `resp.Plugins`, until `resp.NextPageToken == ""` or
   a new bound `setupLanguageCatalogMaxPages = 25` is hit — all inside the
   same overall `setupLanguageCatalogFetchTimeout = 3 * time.Second` deadline
   as before (offline-first: `GET /setup` must still render promptly
   regardless of catalog size).
2. `ListPluginsResponse` gains a custom `UnmarshalJSON`. The real, deployed
   ut-cloud marketplace (grpc-gateway + protojson, default options) sends
   `nextPageToken`/`snapshotVersion` in **camelCase**, with `snapshotVersion`
   as a **quoted JSON string** (protojson quotes int64 to avoid JS float
   precision loss) — but the struct's original tags were snake_case
   (`next_page_token`, `snapshot_version`) with `SnapshotVersion` typed
   `int64`. Decoding a real response therefore always produced
   `NextPageToken == ""`, silently defeating any pagination-following loop
   however correctly written, and would have choked on a quoted
   `snapshotVersion`. The new `UnmarshalJSON` accepts both shapes, preferring
   camelCase when present, falling back to legacy snake_case otherwise.
3. New/changed tests: `TestListPluginsResponse_DecodesLiveWirePaginationFields`,
   `TestListPluginsResponse_DecodesLegacySnakeCasePaginationFields`, a
   strengthened `TestPluginSummary_DecodesLiveWireFormat` (now also asserts
   `NextPageToken`/`SnapshotVersion`), `TestSetupWizardCatalogFollowsPaginationAcrossMultiplePages`
   (5 listings, page size 2 → exactly 3 requests, all 5 show as tiles), and
   `TestSetupWizardCatalogPaginationCapPreventsInfiniteLoop` (a catalog bigger
   than the cap, `GET /setup` still returns within 10s, hits the cap exactly).
   The fake marketplace in `sync_plugins_test.go` gained real offset-based
   pagination (`setCatalogPageSize`) for future tests to build on.

## Independent review

Verdict: **safe to merge, after one fix applied and re-verified below.**

### Verified correct / accepted as-is

- **The wire-format claim is real, not just self-consistent with the new
  tests.** I have read access to `universaltill/ut-cloud` and checked it
  directly: `specs/001-plugin-marketplace/contracts/cloud.proto` declares
  `ListPluginsResponse.snapshot_version` as `int64` (proto snake_case names
  are a source-level convention only); `internal/httpapi/router/router.go`
  wires the API through `grpc-gateway/v2/runtime.NewServeMux()` with no
  `protojson.MarshalOptions` override, so it runs protojson's *default*
  options — lowerCamelCase field names, int64/uint64 emitted as JSON
  strings. The pre-existing fixture
  `internal/plugins/marketplace/testdata/cloud_list_plugins_response.json`
  (captured from a real response, predates this diff) independently
  corroborates this: `"snapshotVersion":"0"` — camelCase, quoted. The fix's
  premise is correct.
- **`defaultPageSize = 20` and `setupLanguageCatalogMaxPages = 25` (≈500
  listings ceiling) is a reasonable, generous bound for a language-pack
  catalog specifically** — one canonical type out of ADR-0002's 20-type
  taxonomy, realistically bounded by the world's actively-maintained written
  languages (low hundreds at the extreme). Confirmed `ListPluginsRequest`'s
  proto (`cloud.proto:76-84`) has **no `page_size` field at all** — the
  client cannot request a larger page to cut round-trips even if it wanted
  to; paging through 20-at-a-time is the only option the server contract
  allows, so the loop is the correct fix, not a workaround for a
  client-side choice that could have been avoided.
- **The 3-second overall deadline covering up to 25 sequential round-trips**
  is a real tradeoff versus the old single-request fetch, and it degrades
  safely: a mid-pagination timeout returns `ctx.Err()` from `ListPlugins`,
  which the loop treats as a hard failure and falls back to
  `c.entries, c.fetched` (old cache / bundled-only) — never a partial,
  silently-incomplete tile list presented as complete. At real-world catalog
  sizes (today: 1-2 pages) and realistic marketplace latency, 3s is not a
  meaningfully tighter bound in practice than before; this only bites if the
  catalog grows into double-digit pages *and* the marketplace is slow, which
  isn't the case today. One documentation nit (not a functional bug): the
  code comment says a timeout "degrades to 'serve what we got'" — the actual
  fallback is the **previous successful fetch's cache**, not the pages
  already collected in the failing attempt (`all` is discarded, not
  returned). The safer behavior (don't show a possibly-incomplete "fresher"
  list without any indication some pages are missing) is the right call;
  the comment's wording is just slightly imprecise. Noted, not worth
  re-touching prose for.
- **Fake marketplace page-slicing math** (`sync_plugins_test.go`): offset
  clamped to `len(entries)`, `end` clamped to `len(entries)`, `nextToken`
  only set when `end < len(entries)`. Traced by hand for 5 entries/page
  size 2: pages `[0:2]→tok"2"`, `[2:4]→tok"4"`, `[4:5]→tok""` — 3 requests,
  matches the test's own assertion. No off-by-one.
- **`firstNonEmptyStr` / camel-vs-snake disagreement**: camelCase wins
  whenever non-empty, by construction (`firstNonEmptyStr(camel, snake)`
  returns the first non-empty argument). Both-present-and-disagreeing is
  unlikely in practice and the code's choice (prefer the modern shape) is
  reasonable and matches the existing `PluginSummary.UnmarshalJSON` pattern
  it says it mirrors.
- **No user-facing string, template, or markup changes** — confirmed by
  diffing the full commit stat against its actual parent: only
  `internal/pages/*.go` (non-test and test), `internal/plugins/marketplace/*.go`,
  and the docs-shots byproduct below are touched. i18n/UX/manual-update
  review rules don't apply here.
- **`web/help/img/**.png` + `manifest.json`**: genuine `make docs-shots`
  regen noise, not a hidden UI change. Diffed the *correct* parent commit
  (the branch's actual immediate parent, not an ancestor several merges
  back — my first attempt used the wrong base and produced a misleading
  wall of unrelated topic-hash churn from intervening merges; redone
  correctly) — the real diff only bumps `surface_sha256` (expected: a
  non-test `internal/pages/**.go` file changed, and that hash covers
  exactly that fileset) with **zero topic-hash changes**. `guard-docs-shots.sh`
  confirms the manifest is fresh against current source. Pulled both
  before/after copies of `web/help/img/en/invoices.png` (the largest byte
  delta, +6 bytes) and viewed them — pixel-identical to the eye; the small
  byte deltas across the three touched PNGs are headless-Chrome PNG-encoder
  nondeterminism (a known, harmless class), not content changes. Neither
  touched topic ("invoices", "translations") has anything to do with the
  setup wizard.
- No real client/shop name, no literal secret, anywhere in the diff
  (grepped the full commit's changed-file diff for common secret/name
  patterns — nothing).
- Repository-pattern / kiosk-engine / plugin-menu-read guards: N/A to this
  diff (no SQL, no `/self-order` routes, no plugin-menu reads touched) —
  confirmed by running the guards, not just by inspection.

### Found and fixed

- **`ListPluginsResponse.UnmarshalJSON`'s `snapshotVersion` handling could
  fail the *entire* response decode on a bare (unquoted) JSON number**, not
  just fail to parse that one field. The original code typed the shadow
  struct's camelCase field as plain `string`:
  `SnapshotVersionCamel string `json:"snapshotVersion"``. Verified with a
  standalone repro: `encoding/json` continues decoding every other field
  when one field hits a type mismatch, but still returns the accumulated
  error from `json.Unmarshal(data, &w)` at the end —
  `json: cannot unmarshal number into Go struct field ...snapshotVersion of
  type string` — and the original code did
  `if err := json.Unmarshal(data, &w); err != nil { return err }`,
  discarding `w.Plugins` and `w.NextPageTokenCamel` (both already correctly
  populated by that point) along with the bad field. Concretely: if
  `snapshotVersion` ever arrived unquoted — a future protojson default
  change, or some intermediate gateway/proxy re-serializing the body — every
  `ListPlugins` call hitting that page would hard-fail, not just lose the
  (currently-unused-for-logic) snapshot version; mid-pagination, that
  aborts the whole catalog fetch and falls back to stale/bundled-only,
  exactly the failure mode this card exists to eliminate, just moved to a
  different trigger. The live wire format today always quotes it (verified
  against ut-cloud's source, above), so this was not an observed bug —
  but it is real, in-scope robustness (this task's own review checklist
  calls out exactly this scenario), one line away from the code just fixed,
  and "validate all external input" is a repo-wide non-negotiable.

  **Fix applied**: `SnapshotVersionCamel` now decodes into `json.RawMessage`
  instead of `string`, so a type mismatch there can never abort the outer
  `json.Unmarshal`. A new helper `parseSnapshotVersionCamel` tries the
  quoted-string shape first (today's real wire format), then a bare number,
  and returns `ok=false` (caller keeps the snake_case/zero fallback) if
  neither parses or the field is absent — matching the same silent-fallback
  posture the snake_case field already has, and consistent with the
  original code's own choice to discard a `ParseInt` error rather than
  propagate it.

  Added `TestListPluginsResponse_TolerantOfUnquotedSnapshotVersion`
  (`{"plugins":[],"nextPageToken":"20","snapshotVersion":42}` — bare
  number) asserting the response still decodes and both `NextPageToken` and
  `SnapshotVersion` come through correctly. Confirmed this test fails
  against the pre-fix shape (reproduced with the standalone repro above,
  matching the exact error string) and passes against the applied fix.

### Independent TDD re-verification (beyond what the tester/dev already claimed)

- Reverted `internal/pages/setup_language_catalog.go` and
  `internal/plugins/marketplace/client.go` to their state on the branch's
  actual immediate parent commit (`3f5f775`, pre-#1108) inside this isolated
  worktree.
- `go vet ./internal/pages/...` failed to build exactly as claimed:
  `internal/pages/setup_language_catalog_test.go:121:50: undefined:
  setupLanguageCatalogMaxPages` (and 3 more references) — a real, on-point
  compile error, not a vague failure.
- `go test ./internal/plugins/marketplace/... -run 'TestListPluginsResponse_Decodes|TestPluginSummary_DecodesLiveWireFormat'`
  failed with the exact claimed symptom:
  `SnapshotVersion = 0, want 1783463941` and
  `NextPageToken = "", want "20"` — confirms the pre-fix decoder really did
  silently zero/empty these fields rather than erroring, which is precisely
  the "loop implemented correctly but decodes NextPageToken as always empty"
  failure mode the commit message describes.
- Restored both files (`git checkout HEAD -- ...`), confirmed
  `go build ./...` clean and every target test green again before doing any
  further work.

## Verification beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean, before and after
  my fix.
- Full `go test ./internal/pages/... ./internal/plugins/marketplace/...`
  (no `-race` on the whole `internal/pages` package per ut-docs#1119,
  tracked separately and unrelated to this diff) — clean, before and after
  my fix.
- `-race -run <new test names>` on just the new/changed tests (both
  packages) — clean, no data race on the shared package-level catalog
  cache or the new decode path.
- Ran the specific CI-blocking guards relevant to this diff's surface
  directly, not just inferred them from the file list:
  `guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh`, `guard-compliance-claims.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` — all pass.
- Cross-repo check against `universaltill/ut-cloud`'s actual proto and
  gateway wiring (not just this repo's own fixtures/tests) for the
  wire-format claim — see above.

## Deferred / accepted, not fixed here

- The `setupLanguageCatalogEntries` comment's "degrades to 'serve what we
  got'" phrasing is slightly imprecise (see above) — cosmetic, not worth a
  separate touch.
- `ListPluginsRequest` has no `page_size` field on the wire contract, so the
  POS client is permanently bound to the server's 20/page default with no
  way to request fewer round-trips — a `ut-cloud`-side API addition, out of
  scope for this repo's fix and not something this diff could have done
  differently.

## Safe-to-merge verdict

**Yes.** Build/vet/fmt clean, full test suite green (including new tests,
with `-race` on the new/changed tests specifically), all relevant CI guards
pass, TDD claims independently re-verified against the branch's real parent
commit, the one real robustness gap found (unquoted-`snapshotVersion` wire
shape aborting the whole decode) fixed and covered by a new regression test,
no i18n/UX/manual impact (none needed — no user-facing surface touched), no
secrets or real client data, docs-shots diff confirmed genuine regen noise
with no visual regression.
