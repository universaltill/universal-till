# Review: `.login-error` banners announced to screen readers (ut-docs#5305)

Branch `fix/5305-login-error-role-alert`. A Sonnet Dev agent built it. A separate Opus Tester agent ran mutations and a driven run. There was no separate UX phase, because the change only adds an attribute; the Tester attested that nothing visible changes (see Verification). An independent Opus reviewer reviewed it in a detached worktree (WIP snapshot `d75a156`).

## What shipped

- `role="alert"` on every conditionally server-rendered `.login-error` banner in `web/ui/**` (18 tags in 17 files): `login.html`, `setup.html:73`, `open_orders.html`, `locations.html`, `kitchen_stations.html`, `promotions.html`, `menu_layout.html`, `fiscal_register.html`, `country_settings.html`, `bluetooth_devices.html`, `registers.html`, `tables.html:6`, `users.html`, `categories.html` (multi-line tag), `channel_order_detail.html` (x2), `partials/table_qr.html`.
- `tables.html:78` `#tables-status`, which starts empty and hidden and is filled by JS, also has `role="alert"`. This is the same shape as `settings.html:3`.
- `setup.html:338` (the tax-plugin skip warning) has no role. It carries a same-line `login-error-role:allow <reason>` marker.
- `internal/pages/login_error_role_alert_5305_test.go` adds two tests:
  - `TestLoginErrorBannersAnnounce_5305` scans `web/ui/**/*.html` and non-vendor `web/public/**/*.js` through `webFiles(t)`.
  - `TestLoginErrorRoleChecker_5305` is a table-driven unit test of the checker.
- `internal/pages/setup_page_test.go:399` now expects the new markup.
- Out of scope, as non-goals:
  - Go-emitted `<span class="login-error">` (in `users_page.go` and `permission_settings_page.go`). These go into containers that already have `aria-live="polite"`.
  - `login.html:143`, which toggles a class on the `#login-exit-os-msg` live region.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| L1 | low | `login_error_role_alert_5305_test.go:22`: `roleAlertRe = \brole="alert"` also matches `data-role="alert"` and `aria-role="alert"`, because `\b` matches after `-`. Such a tag passes with no real role (false negative, confirmed with a probe). Likewise `\bclass="` at `:18` matches `data-class="login-error"` (false positive). Fix: anchor both on whitespace, e.g. `(?:^|\s)role="alert"` against the tag and `\sclass="` in the tag regexp, plus a `data-role` unit case. | Fixed: anchored on whitespace (`\srole="alert"`, `\sclass="`), plus `data-role` and `data-class` unit cases |
| L2 | low | `setup.html:338`: the marker's reason says "static warning rendered with the page, not a response to an action". That is inaccurate. Per `setup_page.go:425-433` and the template comment, the warning is shown once, on the redirect after an explicit POST `/api/setup/tax-plugin-skip`. That is the same full-page-load-after-POST mechanism as `setup.html:73`, which now gets the role. Leaving it without an alert is still defensible: the operator just chose to skip, so it is a deliberate-choice warning rather than an error, and it sits in reading order under the step-4 h1. The reason should say that. Fix: reword to e.g. `login-error-role:allow one-shot warning after the operator's own deliberate skip, not an error; read in order under the step h1`. | Fixed: the reason now says the warning confirms the operator's own skip choice (not an error) and is read in order under the step heading |
| N1 | nit | `loginErrorJSViolation` (`:60`) compiles its regexp on every call. Hoist it to a package `var` next to the others for consistency. Only a few JS files reach that line, so the cost is trivial. | Fixed: hoisted to the package var `jsRoleAlertRe` |
| A1 | info | `channel_order_detail.html:23-24`: the currency and total mismatch warnings are data-state warnings shown on every view of a mismatched order, not a failed-submit re-render. With `role="alert"`, some screen readers announce them assertively on page load. Accepted: the card named them explicitly, a total or currency mismatch on an inbound channel order is something the operator must act on, and the page shows it only for affected orders. | Accepted |
| A2 | info | Checker limits: a single-quoted `class='…'`, a template-built class (`class="{{ if … }}login-error{{ end }}"`) or a `>` inside an earlier attribute value (`x-show="a > b"`, `@click="() => …"`) is not seen (probe-confirmed false negatives). None exist in `web/ui` today, and every repo template uses double-quoted, literal classes. | Accepted (Tester note 1) |
| A3 | info | The JS rule is file-level: one `setAttribute('role','alert')` anywhere satisfies it. Only `inline-actions.js` references `login-error`, and it sets the role on the created line. Inline `<script>` in templates is not scanned (`login.html:143` is the intended live-region exception). | Accepted (Tester note 1) |

Tester note 2 (per-call regexp) is N1.

## Verification

- TDD, re-run by the reviewer in the worktree: removed `role="alert"` from `web/ui/pages/users.html:9`. `TestLoginErrorBannersAnnounce_5305` then failed with `web/ui/pages/users.html:9: .login-error without role="alert" -- add role="alert" ...`. After `git checkout`, `-run LoginError` passes and `git status` is clean. The Tester also confirmed four other mutations (locations, a reasonless setup marker, multi-line categories, JS `'status'`), and the Dev's red run listed 19 offenders.
- Completeness: an independent grep for `login-error` without `role="alert"` across `web/` and the Go sources found only the documented exceptions (`login.html:125` comment, `login.html:143`, `setup.html:338`, CSS selectors, `inline-actions.js:316` which sets the role, and Go-emitted spans into `aria-live` containers).
- Gates in the worktree:
  - `gofmt -l .`: empty.
  - `go build ./...` and `go vet ./internal/pages/`: OK.
  - `go test ./internal/pages/ -run 'LoginError|Setup|OpenOrders|Bluetooth|Elevation'`: OK.
  - The Tester ran the full `go test ./internal/pages/` (OK), plus `guard-i18n`, `guard-rtl-logical-css`, `guard-data-access` and `guard-no-showmodal` (exit 0).
- Driven run (Tester): Playwright/Chromium against baseline and new binaries.
  - Login with a wrong PIN: `role="alert"` is present and the ARIA snapshot contains `- alert`.
  - Setup PIN mismatch: `role="alert"` is present.
  - Geometry and colour are identical before and after, and the screenshots show no visible difference.
- Repo rules: no i18n keys, CSS, `showModal` or offline-path change. Nothing user-visible changes for sighted users.

Pre-flight:
- `web/help/` and `make docs-shots`: n/a. This is screen-reader semantics only; nothing visible changes, and the screenshots were confirmed identical.
- `reference/` page: n/a. Optionally, `reference/ux-guidelines.md` could get one line: "conditional error banners carry `role="alert"`; enforced by `TestLoginErrorBannersAnnounce_5305`".
- `README.md`: n/a.
- `en.json`, meta and language packs: n/a, no keys added.
- Review record: this file.
- Follow-ups: none required.

Verdict: approve. L1, L2 and N1 are fixed. After the fixes the orchestrator re-ran `go vet` and `go test ./internal/pages/ -run 'LoginError|Setup'` (OK), then the full gate: every `ci.yml` build-job guard, `golangci-lint run ./...` and `go test ./...`.
