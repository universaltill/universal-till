# Catalog repository pagination — ut-docs#2149

## What shipped

`internal/plugins/marketplace/catalog_repository.go`'s `CatalogRepository.Fetch`
used to call `cr.client.ListPlugins` exactly once, reading only page 1 of the
marketplace catalog. Same shape as the bug already fixed in this package's two
setup-wizard callers (`resolveAndInstallBasePlugin`,
`languagePackLocalesForListing` — ut-docs#2133/#1108), but broader blast
radius: this fetch backs the general catalog browse UI (`/plugins`), category
listings (`internal/ui/buttons.go`'s `ListActiveCategories`), and tax-code
listings (`internal/pages/plugin_settings_page.go`'s `ListTaxCodes`), not one
bounded lookup. ut-cloud's real `defaultPageSize` is 20, so a listing sorting
past page 1 as the catalog grows was silently invisible everywhere those
three read paths consume the snapshot.

`Fetch` now pages through the full result following `NextPageToken`,
mirroring the exact loop shape of `resolveAndInstallBasePlugin`, bounded by
a new `catalogFetchMaxPages = 25` constant (same value/rationale as the two
existing siblings' own `MaxPages` constants).

## Independent review (Opus, isolated worktree — `complexity:medium` routing)

Ran build/vet/full `go test ./...`/`-race` on the package/golangci-lint/
`guard-data-access.sh`/`guard-i18n.sh`, all green. Independently re-verified
the TDD claim by hand-reverting `Fetch` to single-page and re-running both
new tests — genuine assertion failures (`expected 2 catalog requests ...
got 1`), not compile errors, then confirmed both restore to pass. Confirmed
the loop bound is byte-for-byte the precedent's shape (no off-by-one — the
cap test empirically proves exactly 25 requests) and that no partial
snapshot ever reaches `cr.cached` or disk on a mid-pagination error.

**Findings, all fixed before merge:**

1. **Should-fix — no overall deadline bounded the multi-page loop.** The two
   existing sibling callers rely on their *caller* wrapping the whole attempt
   in a short `context.WithTimeout` (`setupBasePluginAttemptTimeout`,
   `internal/pages/setup_base_plugins.go`) — but `Fetch`'s own callers
   (`internal/pages/plugins_page.go`, the background sync job in
   `internal/server/server.go`) pass a bare, undeadlined request context, and
   `Fetch` itself is what owns the pagination loop. Without a bound, a
   slow-but-alive server that keeps emitting a non-empty `NextPageToken`
   could hold `cr.mu` (blocking every other `Get()`/`Filter()` reader) for up
   to `catalogFetchMaxPages * RequestTimeoutSec` — ~12.5 minutes at the
   config default, against ~30s before this fix. **Fixed**: added
   `catalogFetchTimeout = 30 * time.Second`, wrapping the whole loop in
   `context.WithTimeout`.
2. **Should-fix — AC3 (note the ut-docs#2143 interaction) was unaddressed.**
   **Fixed**: added a doc comment on `Fetch` recording the actual nuance —
   #2143's literal symptom (a dead route) is unchanged, since page 1 still
   fails and returns immediately; the window this pagination loop widens is
   the slow-but-alive case, which `catalogFetchTimeout` now re-bounds.
3. **Should-fix — the two new tests mocked the legacy snake_case pagination
   wire fields** (`next_page_token`, a bare numeric `snapshot_version`)
   instead of the real live wire's camelCase `nextPageToken`/`snapshotVersion`
   (the latter a quoted string — see `ListPluginsResponse.UnmarshalJSON`'s
   own doc comment). A test using only the legacy shape would still pass
   green even if the live-wire decode path (ut-docs#1108) broke. **Fixed**:
   switched both mocks to the camelCase shape, matching the precedent's own
   fake server (`internal/pages/sync_plugins_test.go`).

**Accepted as-is (not fixed, recorded as reviewed):**

4. **Nit — no de-duplication if the page cap is hit against a
   token-repeating server.** Hostile/malformed-server-only; the precedent
   collapses duplicates via its own `best`-selection logic, this snapshot
   doesn't need to because nothing here is selecting a single winner from
   the list. Out of scope for this card.
5. **Nit — pre-existing stale comment in `client.go`** ("Real API uses
   page/page_size instead of page_token") contradicts ut-cloud's actual
   `page_token`-based implementation. Predates this change; not introduced
   by it. Not filed as a follow-up card — a one-line comment fix with no
   user-visible or behavioral effect, not worth a board card's overhead.
6. **Confirmed-by-design — a page-2+ failure with no existing cache now
   returns an error and caches nothing**, where previously page 1 would
   have been saved. This is correct: persisting a truncated catalog as
   authoritative for the 15-minute staleness window is worse than an
   error, and `GetOrFetch`'s stale-cache fallback still applies whenever a
   prior snapshot exists.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./...` clean.
- Full `go test ./...` (whole repo) green, plus `go test ./internal/plugins/marketplace/... -race` green (no races from the added timeout/cancel).
- `golangci-lint run ./...`: 0 issues.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-docs-shots.sh`: all green (this change has no SQL, no user-facing strings, and touches no screenshotted page route).
- Backend-only, no UI surface touched — `ux` skill step and the visual-check attestation don't apply.
- Not independently verified against a real multi-page ut-cloud catalog (no live marketplace with >1 page of listings reachable from this sandbox) — coverage here is via a real HTTP round-trip (`httptest.NewServer`) exercising the actual `Client.ListPlugins` request/response cycle and JSON decode path, not a mocked-at-the-function-level fake.

## Non-goals (explicitly out of scope, per the card)

- Redesigning the fetch's cost/latency tradeoff or `staleAfter` cache
  window — this fix mirrors the existing bounded-pagination pattern, it
  doesn't re-architect the snapshot's refresh strategy.
- Fixing ut-docs#2143 itself (the `/plugins` dead-route 30s block) — only
  the *interaction* is noted, per AC3, not a fix.

## Safe-to-merge verdict

Yes. All should-fix findings resolved; nits accepted with reasoning recorded above.
