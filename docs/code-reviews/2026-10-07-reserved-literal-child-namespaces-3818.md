# Review — reserve core namespaces whose children are literal, not wildcard (ut-docs#3818)

**Date:** 2026-10-07 · **Lane:** cloud-41 · **Complexity:** medium (built by Opus 5.5, reviewed by Fable in a fresh context)

## What shipped

Raised by the independent review of ut-docs#3791, as the same shadowing class with a
different trigger: core roots `/settings`, `/catalog`, `/users`, `/open-orders`,
`/recovery` register only literal multi-segment children (`/settings/menu`,
`/catalog/tax-codes`, …) with no `{wildcard}` and no subtree (`/settings/`)
catch-all of their own. A plugin page registered at a sibling route (e.g.
`/settings/vendor-x`) was unclaimed by any core mux pattern and fell through to
the `/` catch-all (`internal/pages/index_page.go`), rendering as a plugin page
inside a core namespace — the same silent-shadowing shape ut-docs#3786/#3791
already closed for namespaces with `{wildcard}` children.

- `internal/plugins/manifest.go`: `reservedPageRoutePrefixes` gains the five
  roots above. Enforced at install/rollback (`validatePageEntryRoutes`, shared
  by `PersistManifest` and `Rollback`) and at dispatch (`findPageEntry` in
  `internal/pages/plugin_page.go`, which both the `/` catch-all and the
  `/plugin/` subtree resolve through) — one list, all three call sites the
  card required.
- `internal/plugins/page_route_validation_test.go`: new scanner test
  `TestReservedPageRoutesCoverLiteralChildNamespaces`, sibling to the existing
  `TestReservedPageRoutesCoverWildcardNamespaces`, walking every non-test
  `.go` file under `internal/` and asserting that any registered literal
  multi-segment pattern's root segment is reserved — closes this class of gap
  generically (a future unreserved root with a literal child fails CI, not
  just today's five). Plus direct pins in
  `TestPersistManifest_RejectsReservedPageRoutes` and
  `TestReservedPageRoutePrefix` for the five new roots.
- `ut-docs` `reference/plugin-manifest.md` (separate commit,
  `docs/3818-reserved-page-route-namespaces` branch): the `route` field row
  lists the five new reserved roots.

### Decision (BA/Architect, this card's three options)

The card offered three options: (1) reserve the roots outright, (2) reserve
only to the registered literal depth, or (3) invert the rule — refuse any
plugin page route outside `/plugin/…`. Chose (1)/(2) via a generic scanner,
not (3): it's the same mechanism the codebase already uses for the wildcard
case (`reservedPageRoutePrefixes` + a mirrored scanner test), lower-risk than
retiring that whole mechanism, and closes the class for any future root too
— not just the five named here.

## Review findings (Fable, independent)

Verified independently: ServeMux subtree-vs-exact semantics behind the
scanner's skip logic, completeness (hand-classified the full 400+-registration
grep, nothing else is unreserved), no regression against any real plugin
route or test fixture, that dispatch and install/rollback share the one list
(so a pre-existing installed entry stops being served too), full green build/
vet/test, and that the ut-docs doc row matches shipped behaviour.

No blockers, no should-fix items — three nits (comment precision only, no
code change needed):
1. The new test's comment says the gap shape is "literal children *and* an
   exact-match root"; `/recovery` has no exact root of its own and is still
   correctly a gap — comment over-specifies, code is right.
2. Scanner is literal-pattern-only (acknowledged in its own comment, same
   caveat as the wildcard test); confirmed no non-literal registration exists
   today.
3. The updated doc comment on `reservedPageRoutePrefixes` cites `/admin` as
   the single-segment exact-route example; it's actually `GET /admin` —
   harmless.

**Verdict: safe to merge as-is.**

## Verified beyond the automated tests

- `go build ./...`, `go vet ./...` clean.
- `go test -count=1 ./internal/plugins/... ./internal/pages/... ./internal/recovery/...` —
  every package green (re-run independently by both the reviewer and the
  orchestrator, not just the author).
- Hand-classified the full `grep -rnE '\.Handle(Func)?\(\s*"([A-Z]+ +)?(/[^"]*)"' --include='*.go' internal | grep -v _test.go`
  output: the only literal multi-segment roots in the tree are the ones
  already reserved pre-#3786/#3791 plus the five this card adds; `/asset/`
  and `/tags/` are trailing-slash subtree patterns, not gaps.
- Backend-only: no UI surface, no new screen/flow for an operator, customer
  or admin — the UX phase was skipped for this reason (BUILD-CYCLE's "skip
  for backend-only work"), consistent with ut-docs#3786's own review record.
