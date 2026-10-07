# Review — plugin page routes render for GET/HEAD only (ut-docs#3789)

Date: 2026-10-07 · Branch: `fix/3789-plugin-page-get-only` · Lane: `lane:cloud-24`
Built by Sonnet (complexity:easy); reviewed by Opus 5.5 in a fresh context and its own worktree.

## What shipped
- `internal/pages/plugin_page.go`: new `pluginPageMethodAllowed`. GET and HEAD
  render. Any other method gets `405` with `Allow: GET, HEAD`.
- It is called at both dispatch sites, after `findPageEntry` matched: the `/`
  catch-all (`index_page.go`) and the `/plugin/` handler. A path with no page
  entry still 404s for every method. `findPageEntry`, `/` and `/help` are
  unchanged.
- `internal/pages/plugin_page_method_3789_test.go` covers:
  - GET (200, renders) and HEAD (200) on `/faq` (catch-all) and `/plugin/faq`;
  - POST, PUT, DELETE and PATCH on both: 405 with the `Allow` header;
  - POST to `/nope` and `/plugin/nope`: still 404.
- ut-docs `reference/plugin-manifest.md` `route` row documents the rule.

## Findings
1. **minor, accepted.** The 405 is a bare `http.Error`. `guard-page-http-error.sh`
   passes only because it does not follow helpers. It was kept as is:
   - A 405 is a protocol answer, the same one Go's own mux gives for a
     method-bound pattern.
   - Nothing in the product issues a non-GET to a plugin page. The templates have
     no `hx-*`, forms or `method=`, and the plugin content iframe is sandboxed.
   - The kiosk WebView therefore never navigates to it.
2. **nit, accepted.** 405 vs 404 reveals that a route exists. A GET already
   reveals that.
3. **nit, accepted (out of scope).** `/` and `/help` themselves still answer any
   method. That is core behaviour, not plugin shadowing, and stays as is.
4. **process, fixed.** Added this record and the plugin-manifest doc line.

## Verified
- TDD, re-verified by the reviewer in its own worktree. With the fix reverted,
  the test fails 16 times (`POST /faq = 200, want 405`, missing `Allow`, the
  same for PUT/DELETE/PATCH on both routes). With the fix restored it passes.
- Gate: gofmt, `go build ./...`, `go vet ./...`, `go test ./...`, the
  data-access, i18n, page-http-error, kiosk-engine, no-showmodal, core-neutral
  and help-topics guards, and the faq e2e spec.
- No UI surface changed (status codes only), so there was no visual check.

## Deferred
- Install-time refusal of page routes under core wildcard namespaces
  (`/help`, `/orders`, `/journal`, `/plugins`). It extends #3786's
  `reservedPageRoutePrefixes`, so it is filed as ut-docs#3791 (`blocked:dep`
  on #3786).

Verdict: safe to merge.
