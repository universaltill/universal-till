# Review — plugin page routes may not shadow core namespaces (ut-docs#3786)

**Date:** 2026-10-07 · **Lane:** cloud-54 · **Complexity:** easy (built by Sonnet, reviewed by Opus 5.5 in a fresh context)

## What shipped

- `internal/plugins/manifest.go`: `validatePageEntryRoutes` (shared by
  `PersistManifest` and `Rollback`) refuses a `type:"page"` entry whose route is
  one of the core-reserved namespaces, or anything below one:
  - core handler namespaces `/api`, `/ui`, `/v1`
  - core subtrees `/public`, `/ext`, `/plugin-icons`
  - auth-exempt namespaces `/self-order`, `/o`, `/themes`

  The matcher is the exported `ReservedPageRoutePrefix`. It is exact and
  case-sensitive, matching `findPageEntry`'s exact path match, so `/apix` is
  not reserved.
- `internal/pages/plugin_page.go`: `findPageEntry` skips a reserved route at
  dispatch. This is defense-in-depth for a plugin installed before the check
  existed.
- `ut-docs` `reference/plugin-manifest.md`: the `route` row documents the
  rule.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | The first cut missed the auth-exempt namespaces `/self-order`, `/o` and `/themes`. Core registers only specific `GET` patterns under them, so other children fall through to the `/` catch-all, which would serve a plugin page with the POS chrome to anonymous customers and the self-order kiosk. | **Fixed.** All three are added. `TestReservedPageRoutesCoverAuthExemptNamespaces` drives the real `auth.Middleware` over probe paths and fails if an exempt namespace is not reserved. |
| 2 | Low–Med | Plugins already installed with a reserved route would keep being served. | **Fixed.** `findPageEntry` skips reserved routes; covered by `TestFindPageEntry_SkipsLegacyReservedRoute`. |
| 3 | Low | The catch-all renders a plugin page for any HTTP method, and under core wildcard namespaces (e.g. `/help/a/b`, POST `/orders`). These paths are session-gated, and the behaviour predates this change. | **Deferred** to ut-docs#3789. |
| 4 | Low | The comment wrongly called the list "subtree namespaces". | **Fixed.** The comment now names the three groups. |
| 5 | Low | No Rollback-path test. | **Accepted.** Rollback calls the same function, and the existing Rollback test fixture needs a catalog row plus an on-disk versioned manifest. |

The reviewer also confirmed that route normalization cannot bypass the check:
- Go's ServeMux cleans `//`, `.` and `..` with a redirect.
- `%2F` is decoded before matching.
- `/` and `/help` are handled before the plugin lookup.

## Verified beyond the automated tests

- The orchestrator re-ran every TDD claim. With the check disabled:
  - `TestPersistManifest_RejectsReservedPageRoutes` fails with `PersistManifest accepted a page entry on core-reserved route "/api/x"`.
  - With the three auth-exempt prefixes removed, the pin test fails naming `/self-order/…`, `/o/…` and `/themes/…`.
  - With the dispatch skip removed, `TestFindPageEntry_SkipsLegacyReservedRoute` fails with `findPageEntry dispatched reserved route`.
  - All pass once the change is restored.
- A grep of `e2e/`, `plugins/*/plugin.json` and `internal/` fixtures finds no page route that would now be refused.
- Locally, `guard-deadcode-baseline.sh` and `golangci-lint` can't run because the container's tool builds are older than go1.27. Both fail on `main` too, so CI is the gate for them.

## Verdict

Safe to merge once CI is green. This is backend-only, with no UI or locale changes and no manual topic affected.
