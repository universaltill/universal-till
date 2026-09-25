# Review: marketplace catalog cache keyed per (locale, arch) (ut-docs#2674)

- **Branch:** `fix/2674-catalog-cache-per-key`
- **Card:** universaltill/ut-docs#2674 (`complexity:medium`, lane:cloud-24)
- **Author:** Opus 5.5. **Reviewer:** Fable (independent, different model).

## What shipped

`marketplace.CatalogRepository` used to keep one snapshot, in memory and in
`paths.Plugins("cache")/catalog-snapshot.json`, whatever the (locale, arch)
of the request. `/plugins` read it under the UI locale with no arch.
`/plugins/store`, the plugin API and the scheduler read it under the shop
default locale and the device arch. So each page's refresh replaced the
other page's filtered catalog, and the update checker read whichever wrote
last.

- **Per-key cache:** there is one memory slot, one `refreshing` flag and
  one file per key: `catalog-snapshot.<locale|@>.<arch, / → +|@>.json`. The
  monotonic `FetchedAt` guard (ut-docs#2155) now applies per key.
  `coldFetch` already coalesced per key.
- **Key validation:** the locale can come from request input and both key
  parts become a file name. The locale must match `^[A-Za-z0-9_-]{0,35}$`
  and the arch must be `os[/arch]` in `[A-Za-z0-9_]`. Any other key is
  refused before any read, fetch or write, which also rules out path
  traversal. Distinct valid keys can't share a file, because `@` and `+`
  can't appear inside a key part.
- **One till key:** `marketplace.TillCatalogKey(cfg.DefaultLocale)` returns
  the shop locale (falling back to `en-US`, as the scheduler already did) and
  `marketplace.DeviceArch()`. It is used by the store page, both plugin-API
  reads, the update-check handler and scheduler tick (`NewUpdateChecker`
  now takes the key), the background catalog sync (staleness check and fetch
  share it), and `/plugins`.
- **`/plugins` no longer uses the UI locale** (from review finding 2). The
  cloud's `locale` parameter is an inclusion filter. Under a UI locale that
  a plugin doesn't ship, that plugin showed "version unknown" on `/plugins`
  while the store and the update checker still saw the update. Each new
  `?lang=` would also have cost its own cache file and a blocking cold fetch.
- **Upgrade path, offline-first:** the old `catalog-snapshot.json` is still
  read but never written. It is used as a fallback only for the key it
  records, so a till upgraded while offline keeps serving its catalog.
- `SeedSnapshot` writes a snapshot to disk under its own key, for fixtures.
  It never touches the memory map, so the monotonic-clock invariant holds.
- `web/help/img/manifest.json` has a refreshed surface hash. The
  `internal/pages` edits change which cache key is read, and no rendered
  pixel changed.

## Tests

- `catalog_repository_key_test.go` has four tests:
  - Two keys fetched in turn each read back their own catalog, in memory and
    from disk after a restart.
  - Offline, each key serves its own stale snapshot and an uncached key
    serves nothing.
  - The legacy file serves only its own key.
  - Unsafe keys are refused and write nothing, and distinct keys get
    distinct files.
- **Mutation check:** I made `catalogKey` and `snapshotFileName` return one
  constant (the old single slot). The first two tests failed with the
  cross-key symptoms the card describes. After restoring the fix they pass.
- `TestPluginsPage_ReadsTillCatalogKeyNotUILocale`: `GET /plugins?lang=fa`
  requests and caches only the till key.
- I updated the existing seeds to write under the key their reader uses
  (`SeedSnapshot`). `server_test.go`'s `plantSnapshot` still writes the
  legacy file on purpose, and a comment now says so.

## Findings (Fable review)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The scheduler fell back to `en-US` for an empty `DefaultLocale` and the other readers didn't, so they could read different keys | **Fixed:** added `TillCatalogKey`, used everywhere |
| 2 | minor | `/plugins` keyed on `?lang=`: unbounded slots, a cold fetch per new locale, and a locale inclusion filter hiding updates | **Fixed:** `/plugins` reads the till key (with `Cfg` nil-safe, since the handler never needed `Cfg` before); new test |
| 3 | minor | ADR-0112 (ut-docs) still describes the single file as the cache and the single slot as open | **Fixed** in a ut-docs PR with this change (a factual note, no decision changed) |
| 4 | nit | Per-key file trusted without checking its recorded key (a case-insensitive filesystem maps `en-US` and `en-us` to one file) | **Fixed:** per-key and legacy reads both check the key recorded in the file |
| 5 | nit | `plantSnapshot` silently exercises the legacy path | **Fixed:** added a comment |
| 6 | nit | The legacy file is never deleted and is re-parsed on a cold miss | **Accepted:** it's small, read only on a cold per-key miss, and deleting it would drop the offline fallback for a till that hasn't fetched yet |

The reviewer also checked and found sound: map access under `cr.mu`, the
per-key monotonic guard and its invariant, no traversal or collision,
`MkdirAll` before writes (with `cacheDir` from `paths.Plugins("cache")`),
and every caller now reading a consistent key.

## Gate

`gofmt -l .` (clean), `go build ./...`, and `go test ./...` all pass.
`go test -race` passes for `internal/plugins/marketplace` and
`internal/server`. For `internal/plugins`, `-race -run
'Update|Catalog|Compare'` passes. The whole package under `-race` hit the
default 10-minute timeout in an unrelated slow test
(`TestPersistManifest_…`); CI already runs that package with
`-timeout 20m`. `golangci-lint run ./...` reports 0 issues. Every
`build`-job guard passes except `guard-shellcheck-version.sh`, because this
container has no `shellcheck` binary; no shell script changed.

## Tester attestation

The change is backend only: which catalog slot each page reads. No template,
CSS or JS changed and no rendered pixel changed; the docs-shots guard passes
on the refreshed hash. I didn't look at any visual surface. No Playwright spec
covers `/plugins` or `/plugins/store`. Handler tests run through the real
mux cover the behaviour.

## Follow-ups

- ut-docs#2673 (send `device_arch` and `host_version`) is unblocked. Once
  `arch=` is honoured, every till-side reader already asks for the same
  arch.

**Verdict:** safe to merge.
