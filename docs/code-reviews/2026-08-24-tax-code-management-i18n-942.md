# Code review: `tax_code_management` permission label has no locale key (ut-docs#942)

## What shipped

Migration `internal/db/migrations/057_tax_code_management_permission.sql`
added the `tax_code_management` permission action to the catalog, but
unlike every other dedicated action, `web/locales/{en,ar,fa,tr}.json` had
no matching `permissions.action.tax_code_management` key. The
permission-matrix editor renders action labels via a Go-side dynamic
lookup (`httpx.T(locale, fmt.Sprintf("permissions.action.%s", action))`,
`internal/pages/permission_settings_page.go`) rather than a static
template `{{ T "..." }}` call, so `scripts/ci/guard-i18n.sh` — which only
scans `web/ui/**/*.html` for literal `T "key"` calls — has no visibility
into this class of gap, and `httpx.T` silently falls back to the raw key
string instead of failing loudly. The bug was invisible except by
actually looking at the rendered page.

Fix: added `"permissions.action.tax_code_management"` to all four locale
files, in the same chronological position as the other
`permissions.action.*` keys (after `stock_location_management`, the most
recently added). Each locale's value reuses that locale's existing
`taxcodes.title` string verbatim (en: "Tax codes", ar: "رموز الضريبة",
fa: "کدهای مالیاتی", tr: "Vergi kodları") for terminology consistency
with the tax-codes management page itself, rather than a fresh
translation.

Added a regression test,
`TestPermissionSettingsPage_GET_TaxCodeManagementHasTranslatedLabel`
(`internal/pages/permission_settings_page_test.go`), asserting the
rendered matrix page contains the translated label and never the raw
`tax_code_management` fallback string.

## Independent review

Fresh-context Sonnet subagent (complexity:easy → Sonnet review, per the
model-routing rubric), isolated worktree. Verdict: **safe to merge,
no findings** (no blocker/should-fix/nit).

What it verified independently, not taken on trust:
- All 4 locale files still valid JSON, key sets exactly match `en.json`
  (1581 keys, no drift), and the new key lands at the identical line
  position in all four.
- Each locale's new value is character-for-character identical to that
  locale's existing `taxcodes.title` — confirms the stated
  terminology-consistency rationale rather than an approximate
  paraphrase, and means no new, unvetted translation text was
  introduced.
- `scripts/ci/guard-i18n.sh`, `go build ./...`, `gofmt -l .` all clean.
- **TDD claim re-verified independently**: removed just the new line
  from `en.json`, re-ran the new test — real assertion failure (raw
  HTML fallback in the response, not a compile error or unrelated
  panic). Restored the fix — test passes again.
- Broader `internal/pages` package suite: no collateral breakage.
- Manual/help topics: concluded no update needed — this fixes a
  rendering bug on an already-documented screen (the permissions matrix
  and tax-codes pages both already have help topics), adds no new route
  or capability a shop owner would perceive as new.
- Scope check: nothing unrelated in the diff; all 4 locale files
  present; key spelling matches the migration's catalog action exactly.

## Verified beyond automated tests

- Personally re-ran the same revert/restore TDD sequence a second time
  (removed `en.json`'s line, confirmed the new test fails; restored,
  confirmed it passes) before handing to the reviewer subagent, matching
  its result.
- Ran the full CI-blocking guard suite locally (all 16 guards in
  `.github/workflows/ci.yml`'s `build` job — data-access, kiosk-engine,
  plugin-menu-read, i18n, compliance-claims, docs-shots, help-topics,
  webkit-version, kiosk-launch-flags, android-status-address,
  android-i18n, emoji-font, htmx-loaded, autofill-suppression,
  brand-assets, makefile-version): all green.
- Ran the full test suite matching CI's actual invocation exactly —
  `go test $(go list ./... | grep -v '/internal/plugins$')` plus
  `go test -timeout 20m ./internal/plugins` separately (no `-race`, per
  the CI workflow's own comment on why — this package's default 600s
  timeout is margin-tight, not a real hang, ut-docs#643/#753/#776):
  all packages pass, `internal/plugins` in 78.7s.

## Deferred / explicitly out of scope

- `guard-i18n.sh`'s blind spot for Go-side dynamic `fmt.Sprintf("...%s",
  action)` locale-key lookups (as opposed to static template `T "key"`
  calls) is confirmed real by this investigation, but fixing the guard
  itself is separate scope from this card (which only asked to add the
  missing key and confirm the guard's coverage). Not filed as a new
  backlog card here — flagging in this record for visibility; a future
  card could teach the guard to cross-reference the DB-seeded
  `permission_actions` catalog against `permissions.action.*` locale
  keys if this class of gap recurs.

## Safe-to-merge verdict

Safe to merge. No findings from independent review, all CI-blocking
guards green, full test suite green (matching CI's real invocation),
TDD claim re-verified twice independently.
