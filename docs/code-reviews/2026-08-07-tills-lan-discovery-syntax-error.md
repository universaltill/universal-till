# Tills page LAN discovery dead button — inline `<script>` syntax error (ut-docs#373)

## What shipped

`web/ui/pages/tills.html`'s inline `<script>` block (ADR-0033 LAN discovery
+ approve-to-pair, ut-docs#183/#184/#185/#289) had an unbalanced brace: the
`.then(function (j) { ... })` callback in the `fetch('/api/sync/discover-
primaries')` chain was never closed before `.catch(...)`. A `<script>`
block that fails to parse never executes *any* of it, so `#discover-btn`'s
`addEventListener` wiring never ran — clicking "Find a primary on this
network" on Settings → Tills did nothing at all, silently, in production
since this shipped. The backend endpoints
(`/api/sync/discover-primaries`, `/api/sync/pair-start`,
`/api/sync/pair-status`) were always fine and are Go-level tested directly;
nothing exercised the page's own inline script in a real JS engine, which
is exactly how this slipped through.

Fix:

- `web/ui/pages/tills.html`: added the missing `})` closing the `.then()`
  callback before `.catch(...)`. One-line fix, verified with `node --check`
  against the extracted script (Go `{{ T "…" }}` expressions substituted
  with placeholders first, since those aren't valid JS pre-render) — fails
  with `SyntaxError: Unexpected token '.'` at HEAD, parses clean after the
  fix.
- `e2e/tests/tills-lan-discovery.spec.ts` (new): real-browser Playwright
  coverage that clicks the button and drives the actual fetch chain to one
  of its two genuine terminal states (a rendered `<li>` result with a
  "Request to pair" button, or the explicit "No primaries found on this
  network." message) — the gap that let this ship (nothing loads this
  script in a real JS engine) is exactly what this closes. Wrapped in the
  existing `watchConsole()` helper so any uncaught JS error (including the
  original SyntaxError) independently fails the test.
- **Sweep**: audited all 33 `web/ui/pages/*.html` files for the same class
  of bug (extract each inline `<script>`, substitute `{{ }}` template
  expressions with placeholders, `node --check`). Only `tills.html` was
  broken — confirmed both before the fix (fails) and after (all 33 clean).

## Independent review (Sonnet, fresh context)

Verdict: **PASS**, one minor flakiness risk found and fixed before commit.

Independently re-ran and confirmed both TDD-shaped claims: `node --check`
fails on the pre-fix script and passes post-fix; the 33-file sweep
reproduces "only tills.html broken" on both sides. Read the new e2e spec
in full and judged it non-tautological — the `"Searching…"` intermediate
assertion only ever fires if the click listener was attached at all
(load-bearing on the fix, not just a smoke check), and the two accepted
terminal states are mutually exhaustive of the real success outcomes; the
pre-fix state (permanently stuck on "Searching…", button never
re-enabled) times out and fails, not silently passes. Confirmed
`esc()`/`escAttr()` XSS-escaping on rendered primary name/till_id is
unchanged by the diff. Confirmed no new user-facing strings (no locale
files touched) and no repository-pattern/money/offline-first implications
(LAN-only, manager-gated feature, not checkout).

One finding, fixed before commit:

1. **Timeout margin too tight** — `await expect(btn).toBeEnabled({ timeout:
   6000 })` left only ~2s of headroom over the server-side 4s
   `discoverBrowseTimeout` (`internal/pages/discovery_api.go`) for the HTTP
   round trip, JSON decode and render under CI load. Bumped to 10s.

## Verified beyond automated tests

- Ran the new spec against a real, fully-booted till (both the
  `default`/`auth` Playwright projects boot concurrently per
  `e2e/playwright.config.ts`) — confirmed it actually finds the sibling
  `auth`-project till via a real mDNS broadcast on the sandbox's shared
  network namespace (both instances are standalone/primary by default and
  advertise themselves), landing in the "found a result" branch, not just
  the "none found" one — so both of the spec's accepted terminal states are
  exercised by the real environment, not merely written into the assertion.
- Ran the full existing `e2e/` suite (61 specs, `default` project): 60
  passed including the new spec; the one failure
  (`catalog-image-to-till.spec.ts`) reproduces byte-identically on
  unmodified `main` (confirmed via a targeted revert-and-rerun) — a
  pre-existing, unrelated image-load flake, not caused by this change.
- Confirmed `go build ./...`, `go vet ./...`, `internal/pages/...` and
  `internal/discovery/...` package tests, `guard-data-access.sh`,
  `guard-i18n.sh`, and `guard-help-topics.sh` all pass. Full
  `go test ./... -race` run separately (see PR/CI for the complete gate
  result — no Go source was touched by this diff, only a template file and
  a new e2e spec).
- `gofmt -l` shows pre-existing drift in 4 unrelated files
  (`internal/pages/external_api_test.go`, `internal/pages/import_page_test.go`,
  `internal/plugins/marketplace/client.go`,
  `internal/thirdparty/webview_go/webview.go`) — none touched by this diff;
  tracked separately by ut-docs#318.

## Explicitly out of scope

- The pre-existing `catalog-image-to-till.spec.ts` flake — unrelated,
  reproduces on `main`, not filed as a new card here (no evidence yet
  it's anything beyond image-load timing in this sandbox).
- No user manual update: this restores previously-shipped, already-
  documented-as-existing button behavior to working order — no new
  route, page, or shop-owner-visible surface was added or altered.

## Safe-to-merge

Yes. `go build`, `go vet`, package tests for the touched area, the full
`e2e/` Playwright suite (default project), `guard-data-access.sh`,
`guard-i18n.sh`, and `guard-help-topics.sh` all green; the one e2e failure
observed is pre-existing and unrelated (confirmed via revert-and-rerun).
