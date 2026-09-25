# Review — Plugin Store: Official badge from the catalog, stale banner removed (ut-docs#2647)

**Branch:** `fix/2647-official-trust-badge` · **Pair:** ut-cloud `fix/2647-official-trust-tier`
**Author:** Opus 5.5 (pipeline, lane:cloud-24) · **Reviewer:** Fable (independent subagent)

## What shipped
- `trustTierOf` (`internal/pages/plugins_store_page.go`) maps the catalog trust
  level `official` → the gold Official badge (no consent prompt). The live
  catalog's `id` is the listing UUID, so the old `com.universaltill.` id-prefix
  rule never matched and every official plugin showed ⚠ Unverified.
- `web/ui/pages/plugins_store.html`: removed "Showing the full catalog
  (marketplace approval filter unavailable)". `EntitledFiltered` is always
  false, and browse intentionally lists the whole public catalog (access is
  enforced at download/install), so the note was wrong on every visit.
- The store test fixture now uses UUID ids like the real cloud (the old fixture
  used slugs, which is why the bug passed CI); new `TestTrustTierOf`.
- `docs/market_place_openapi.yaml` lists `official`. `web/help/img/manifest.json`
  surface hash refreshed only — `/plugins/store` has no manual screenshot
  (Docs-Shots-Unchanged). Help topic `plugins.md` already describes the badges
  correctly.

## Findings (shared review with the ut-cloud half)
- Dead locale key `plugins.store.unfiltered_note` (nit) — left in place:
  no guard flags it, and removing it would need de/es pack PRs for no gain.
- Other findings are cloud-side; see ut-cloud
  `docs/code-reviews/2026-09-25-official-trust-level.md`.
- Tills on ≤ v0.21.x keep showing Unverified until they update (their
  `trustTierOf` doesn't know `official`); no regression for them.

## Verified
- TDD: reverting `plugins_store_page.go` + `plugins_store.html` fails
  `TestPluginStoreShowsCatalogForAnonymousTill` (3 asserts) and `TestTrustTierOf`
  (author and reviewer, independently).
- `gofmt`, `go build ./...`, `go test ./...` green; every `ci.yml` build-job guard
  passes (shellcheck-version needs a shellcheck binary this container lacks).
- **Driven run:** built binary, `UT_AUTH=off`, demo seed, pointed at a mock
  catalog sending UUID ids with `trustLevel` official/official/unverified.
  Looked at `/plugins/store` in English 1280×800 and Persian (RTL) 1024×600:
  two gold "Universal Till" badges, one ⚠ Unverified, no banner, nothing
  clipped or overlapping. A dark-scheme browser renders identically (the till
  theme doesn't follow the OS scheme). Not looked at: German, kiosk portrait.
  Side effect: the header card now holds only the "Manage installed plugins"
  button when the catalog loads; acceptable.

**Verdict:** safe to merge.
