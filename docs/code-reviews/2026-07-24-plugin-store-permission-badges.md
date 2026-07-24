# 2026-07-24 — Permission and manager-approval badges on the plugin store (FR-006)

## Context
`ut-docs/QUEUE.md` gap: "No marketplace audit-trail UI page; no
permission/manager-approval badges on the store page." The audit-trail page
shipped earlier this session (PR #47); this closes the badges half.

Depends on a same-day `ut-cloud` contract change (0.0.6,
`docs/code-reviews/2026-07-24-permission-badges-contract.md` in that repo)
that added `PluginSummary.permissions` — a plugin's manifest-declared
capability scopes (e.g. `net:api.stripe.com`, `storage`), extracted
server-side and exposed on the catalog.

## Design
- `internal/plugins/marketplace/client.go`: `PluginSummary.Permissions`
  wired through `UnmarshalJSON`'s wire struct.
- `internal/pages/plugins_store_page.go`: `storeItem.Permissions`, copied
  from the catalog snapshot plugin in `PluginStoreHandler`.
- `web/ui/pages/plugins_store.html`: each declared permission renders as a
  badge on the card, next to the description. A manager-approval notice
  (🔒 "Installing requires manager approval") renders on every
  not-yet-installed card — static/unconditional rather than per-plugin
  conditional, because PR #46 (earlier this session) already made manager
  approval a blanket requirement on every store mutation (download,
  install, delete-download), so there's no "sometimes gated, sometimes
  not" case to represent.
- i18n keys (`plugins.store.manager_approval_notice`,
  `plugins.store.permission_hint`, `plugins.store.permissions_label`) added
  to all 4 locales (en/ar/fa/tr).

## Independent review
Opus-model review, adversarial brief (verify independently, don't trust the
implementer's summary).

**Confirmed correct (reviewer verified independently):**
- `UnmarshalJSON`'s new `p.Permissions = w.Permissions` assignment is
  clean — doesn't interact with the existing
  `Capabilities`/`RequiredCapabilities` fallback logic on either side.
- The manager-approval notice's claim is actually true today, verified by
  reading the route handlers directly rather than trusting the commit
  message: all three store-card actions
  (`/api/plugins/store/{download,install,delete-download}`) open with
  `isManagerOrAuthOff(r)` gate checks (`plugins_store_page.go:196,227,263`),
  and a dedicated gate-rejection test already covers all three.
- Template diff is well-formed (`{{ if }}`/`{{ range }}`/`{{ end }}`
  balance, confirmed by template parsing succeeding) and safe from XSS —
  `.Permissions` values render through default `html/template`
  auto-escaping, no `template.HTML` cast anywhere in the diff.
- `scripts/ci/guard-i18n.sh` re-run independently: green, all 4 locales
  match en.json's key set including the 3 new keys.
- `go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh` all
  re-run independently and confirmed green.
- The new test (`TestPluginStoreRendersPermissionAndManagerApprovalBadges`)
  is non-vacuous: three cards (permissioned / no permissions / installed)
  and the assertions (`perm-badge` count == 2, approval-notice count == 2)
  would each fail on a real regression, not just pass by construction.
- RTL-safe: new inline styles use only `gap`/`flex-wrap`/`align-items`/
  block-axis `margin`, no hardcoded `left`/`right`.

**No findings requiring a fix.**

## Verification
`go build ./...`, `go test ./...`, `bash scripts/ci/guard-i18n.sh`,
`bash scripts/ci/guard-data-access.sh` — all green, both by me and
independently by the reviewer. New test
`TestPluginStoreRendersPermissionAndManagerApprovalBadges` added to
`internal/pages/plugins_store_page_test.go`.
