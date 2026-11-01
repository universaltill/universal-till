# Review: plugin settings Permissions table shows plain-language descriptions (ut-docs#3553)

Date: 2026-11-01 · Branch: `feat/3553-plugin-settings-permission-descriptions` · Lane: `lane:cloud-41`
Author: Sonnet (Dev subagent) + orchestrator (ar/fa/tr help, test tightening) · Reviewer: Opus 5.5, fresh context

## What shipped

- `web/ui/pages/plugin_settings.html`: in the Permissions table's name cell, a
  permission with a `permissionBadge.DescKey` (the same lookup the store
  detail page uses — `internal/pages/plugin_permission_view.go`) now renders
  its raw name as `<code dir="ltr">` followed by the description as visible
  text (`<span class="perm-desc muted">`, existing `.perm-desc` class —
  block, block-axis spacing only). Permissions without a description and
  setting-bound grants render exactly as before. No Go change, no new
  i18n key: `permissionView` already embedded `permissionBadge`.
- Test `TestPluginSettingsPage_GET_PermissionsShowPlainLanguageDescriptions`
  (`http:lan`, `view:sales` described; `sales:read` plain).
- Help: `web/help/{en,de,ar,fa,tr}/plugins.md` step 4 gains one sentence —
  a permission the store page explains in plain words shows the same
  explanation under its name here. `web/help/img/manifest.json` refreshed by
  `make docs-shots` (no PNG changed).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | ar/fa/tr `plugins.md` carry the same Permissions passage but only en/de got the new sentence (drift guard compares structure only). | Fixed: translated sentence added to ar, fa, tr. |
| 2 | Low | The "no description" assertion located `sales:read` by its first occurrence anywhere in the page — could test the wrong spot and pass vacuously. | Fixed: it now locates the exact plain name cell `<td style="font-family:monospace">sales:read</td>`. |

Reviewer also checked: test strength (exact span inside that permission's
own cell; both descriptions contain an apostrophe, so escaping is exercised),
auto-escaping of `T` output, setting-bound branch untouched, `.perm-desc` RTL
safety, the help sentence does not overclaim (AI data-access permissions are
in `permissionDescKeys`), no `showModal`, no new keys.

## Verified

- TDD re-verified by the orchestrator: template reverted to `HEAD` → the new
  test fails (`expected http:lan as <code dir="ltr"> name`); restored → passes.
- Visual: the real handler's HTML (en, de, fa) rendered with `app.css` in
  Chromium at 1024×600 and 360×740 and the screenshots read: description
  sits under the name, the table does not scroll sideways (table 511px in a
  542px card at 1024; 285px in 316px at 360), fa is right-aligned with the
  code name kept LTR. Not checked: real touch hardware (no touch-specific
  behaviour changed), dark theme.
- Gates: gofmt, build, vet, `go test ./...`, golangci-lint, every
  `ci.yml` build-job guard. Local-only failures, environmental and unrelated:
  `guard-deadcode-baseline` (local deadcode binary built with go1.26 vs the
  module's go1.27) and `guard-shellcheck-version` (no shellcheck in the
  container); CI runs both.

## Verdict

Safe to merge. Deferred: none.
