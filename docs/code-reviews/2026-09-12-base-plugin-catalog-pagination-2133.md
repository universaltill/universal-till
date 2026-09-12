# Code review: country base-plugin auto-install pages the full catalog (ut-docs#2133)

**Date:** 2026-09-12
**Author:** pipeline (Sonnet, `complexity:easy`)
**Reviewer:** independent Sonnet subagent, fresh context, isolated worktree
**Verdict:** safe to merge, no blockers

## What shipped

`internal/pages/setup_base_plugins.go`'s `resolveAndInstallBasePlugin`
fetched only page 1 of the marketplace catalog via `client.ListPlugins`,
unlike its two setup-wizard siblings (`setupLanguageCatalogEntries` in
`setup_language_catalog.go`, and the equivalent in `setup_tax_catalog.go`),
which both already follow `NextPageToken` across the full result
(ut-docs#1108). ut-cloud's real catalog paginates at ~20 listings/page and
compatibility filtering runs server-side before pagination, so a
legitimate country base-plugin match can sort past page 1 as the catalog
grows — and because "nothing published for this country yet" is already a
legitimate, silent no-op one level down, an unpaginated fetch would
silently install nothing and report no error at all.

Fix:
- Added `setupBasePluginMaxPages = 25`, mirroring
  `setupLanguageCatalogMaxPages`/`setupTaxCatalogMaxPages` exactly (same
  value, same reasoning: generously covers any real catalog while
  bounding a malformed/hostile server's infinite-token loop).
- `resolveAndInstallBasePlugin` now accumulates every page into a slice
  before filtering by `CanonicalType`/locale and picking the
  highest-semver match, following `NextPageToken` under the same overall
  `ctx` deadline the single-page call used before (no timeout-semantics
  change).
- Everything downstream of the fetch (idempotency check via
  `InstallStatusStore`, `cloudInstallPluginVersion`, the language-locale
  catch-up) is untouched.

## Tests

New regression tests in `internal/pages/setup_base_plugins_test.go`:
- `TestResolveAndInstallBasePlugin_FindsListingBeyondFirstCatalogPage` —
  catalog page size 1, an unrelated "es" listing on page 1, the real "de"
  match on page 2; asserts the plugin still installs and exactly 2
  catalog requests were made.
- `TestResolveAndInstallBasePlugin_PaginationCapPreventsInfiniteLoop` — a
  catalog that never returns an empty `next_page_token`; asserts the loop
  terminates at exactly `setupBasePluginMaxPages` requests within a 10s
  timeout.

Both were confirmed to fail against the pre-fix code (build-time failure
initially, since the test file references `setupBasePluginMaxPages` before
it exists; then, in the independent review's own revert-then-restore pass
against a version with the constant kept but the pagination loop reverted,
genuine runtime failures with the exact claimed symptoms — see below) and
pass after the fix.

## Independent review

A fresh-context Sonnet subagent reviewed the diff in an isolated git
worktree (`isolation: "worktree"`, per the `reviewer` skill), with no
visibility into the implementer's reasoning.

**TDD re-verification: PASS.** The reviewer reverted only the pagination
loop (kept the constant so the test file still compiled), rebuilt, and
reran the two new tests:

```
setup_base_plugins_test.go:107: expected ut-plugin-language-de (catalog page 2 at page size 1) to be installed and active — an unpaginated fetch would silently miss it and report no error
--- FAIL: TestResolveAndInstallBasePlugin_FindsListingBeyondFirstCatalogPage (0.10s)

setup_base_plugins_test.go:145: expected exactly 25 catalog requests (cap), got 1
--- FAIL: TestResolveAndInstallBasePlugin_PaginationCapPreventsInfiniteLoop (0.09s)
```

Restored the fix; both passed again, confirmed identical to the reviewed
commit via `git diff` (no output).

**Findings:** no blockers.

- *Non-blocker*: the pagination loop now runs inside the same
  `setupBasePluginAttemptTimeout` (5s) that previously bounded a single
  catalog call plus the install download, on the foreground
  `installBasePluginsForSetup` path. As the real catalog grows, more of
  that budget goes to pagination before install even starts, making a
  fall-through to background retry somewhat more likely. This is the same
  deliberate tradeoff the sibling implementations already accept (same
  comment pattern: "under the same ctx deadline as before") — offline-first
  is preserved (ctx cancellation still bounds it, never blocks
  indefinitely) — recorded here, not changed.
- `best` pointer safety across the multi-page accumulation: confirmed
  safe — `all` is fully assembled before any `&all[i]` address is taken,
  and `all` is never mutated/reallocated afterward.
- Off-by-one check on the loop bound and the exact-request-count test
  assertion: both correct, no off-by-one.
- Test-tautology check: `TestResolveAndInstallBasePlugin_
  FindsListingBeyondFirstCatalogPage` genuinely fails without the fix
  (verified directly, not inferred).
- `guard-data-access.sh`: N/A (no SQL added outside `internal/data`/
  `internal/db`) — ran directly, passed.
- i18n: N/A — backend Go logic only (log messages), no user-facing
  template/JS strings touched.
- No real client/shop name or secret-shaped literal in the diff.
- No UI surface touched — the UX-guidelines and user-manual/help-topic
  checklists don't apply to this change.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `golangci-lint run ./...` (whole repo) — 0 issues.
- Full `go test ./...` — every package green, including `internal/pages`
  (264s) and its subpackages.
- `bash scripts/ci/guard-data-access.sh` and `bash scripts/ci/guard-i18n.sh`
  — both pass.
- Independent reviewer separately re-ran build/vet/gofmt/lint/tests in its
  own isolated worktree with matching results.

## Audit follow-up (out of scope for this card)

Acceptance criterion 4 asked to audit for other single-page `ListPlugins`
calls with the same shape. Found `internal/plugins/marketplace/
catalog_repository.go`'s `CatalogRepository.Fetch`, which backs the
general browsable catalog (`ListActiveCategories`, `ListTaxCodes`, the
background catalog refresh, `/plugins` browsing) — architecturally
different (a snapshot-with-TTL cache feeding several unrelated read paths,
not a single resolve-and-install call), so fixing it needs its own design
pass on paging cost vs. the background refresh cadence. Filed as
ut-docs#2149 rather than folded into this fix.

## Deferred / explicitly out of scope

- ut-docs#2149 (above).
