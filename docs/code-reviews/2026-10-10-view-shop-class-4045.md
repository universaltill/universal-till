# Review: `view:shop` class and `shop.context.v2` (ut-docs#4045)

**Date:** 2026-10-10 · **Lane:** lane:cloud-41 · **Author model:** Opus 5.5 ·
**Reviewer model:** Sonnet 5.5, fresh context. Fable was the mapped reviewer
for a hard card, but it was refused (out of usage credits), so Sonnet
reviewed instead, as `MODEL-ROUTING.md` allows.

## What shipped

- `internal/data/core_views.go`: `shop.context.v2` is registered under a new
  non-★ class, `view:shop`. It returns the same `ShopContextRow` and uses the
  same provider as v1, through a shared `runShopContext(name)` whose
  missing-provider error names the view. `shop.context.v1` stays under
  `view:sales`.
- `internal/pages/plugin_permission_view.go`: a consent line for `view:shop`
  (`plugins.permissions.desc.view_shop`) in en, ar, fa and tr. It says what the
  class reads and that it never reads sales, staff or customers.
- Tests:
  - the registry/permission map and the result-shape pin (v2);
  - v1 and v2 rows and errors;
  - the real provider for both versions;
  - a WASM guest: `view:shop` reads v2 and is denied, with an audit row, on
    v1 and `sales.receipts.v1`; `view:sales` is denied on v2;
  - the consent key in every locale.
- README and `docs/plugin_guidelines.md` list the class and the view.
- `web/help/img/manifest.json`: only the surface hash changed. No screenshot
  shows a plugin that declares `view:shop`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | No review record yet | This file |
| 2 | nit | `ShopContextRow`/`ShopContextFunc`/`SetCoreViewShopContext` and `shop_context_view.go` comments named only v1 | Fixed |
| 3 | nit | No test enforces the ADR rule that `view:shop` gates only receipt-printed or on-screen shop facts | Accepted: policy, enforced by the ADR and review |

The reviewer found no correctness, security or docs-mismatch issues. It
checked ut-cloud: `hostabi.AnyView`, `pkg/manifest` and `permlint` name no
view class, so they accept `view:shop` / `shop.context.v2` unchanged.

## Verified

- The reviewer ran `go build`, `go vet`, data and pages tests,
  `go test -timeout 20m -run 'ViewQuery|Permission|Manifest' ./internal/plugins/`,
  `guard-i18n.sh` and `guard-data-access.sh`. All passed.
- The reviewer re-verified the TDD claims in a separate worktree:
  - Reverting `core_views.go` makes the data tests fail (v2 not registered).
  - Reverting `plugin_permission_view.go` makes the permission test fail.
  - Both pass again once restored.
- Author ran the full gate:
  - gofmt and golangci-lint: 0 issues.
  - Every `build`-job guard passed. `guard-shellcheck-version` was the one
    exception: there is no shellcheck binary in the container.
  - `go test ./...` passed. `internal/plugins` hit the 10-min default
    timeout, as expected, so it was re-run with CI's `-timeout 20m`.
- Visual: no screen layout changed. The new consent line renders through the
  existing badge description path, the same one `view:users` uses. I did not
  look at it in a browser: no installable plugin declares `view:shop` yet.

## Verdict

Safe to merge. Follow-ups in the language packs: `ut-plugin-language-{de,es,pt}`
`i18n/4045-view-shop-permission`.
