# Review: promotion customer picker gated on `settings` (ut-docs#5589)

**Date:** 2026-11-05 · **Card:** universaltill/ut-docs#5589
**Author:** Sonnet (Dev subagent) · **Reviewer:** Opus 5.5 (independent subagent, different model)

## What shipped

The New/Edit promotion dialog's "only for this customer" search called
`GET /api/data/customers`. That is the GDPR erasure picker, gated on
`data_management` and returning email. So a custom role (ADR-0128 §3), or an
edited built-in grant, holding `settings` without `data_management` could open
Promotions but got a 403 notice on typing a name. Built-in roles hold both
actions, so they never hit it.

- `GET /api/promotions/customers?q=` (`internal/pages/promotions_page.go`),
  inside `registerPromotions`. It is gated by `canPerform(d, r, "settings")`,
  the same action as the page's `requireManager`, but answers in JSON
  (`dataAPIRespond`, existing key `common.error.manager_or_admin_required`),
  because the dialog script does `r.json()`. A 500 is logged and answers
  `common.error.server`, never `err.Error()`. A 200 returns
  `{"data":{"customers":[{"id","name","phone"}]},"error":null}`, and an empty
  result is `[]`.
- `data.CustomerPick` + `POSRepo.SearchCustomersForPicker`
  (`internal/data/pos_repo.go`). It matches name (SearchFold) or phone and
  never selects or matches email. A blank query returns an empty list without
  running a query. The limit is clamped to 25.
- `web/ui/pages/promotions.html` calls the new route and reads the lowercase
  fields. The 401→/login and `j.error` handling is kept.
- `demo_mode.go` allows the new GET. `/api/data/customers` and `/erase` are
  unchanged.
- Help: `web/help/en/promotions.md` step 3 and `users.md`, the custom-role
  paragraph. `manifest.json`: the surface hash and the `promotions`/`users`
  `en` topic hashes changed. The PNGs are unchanged, because there is no
  visible UI change.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | medium | `phone LIKE ?` did not escape `%`/`_`. `q=%` (or `_`) passed the blank-query guard and listed every customer with a phone (up to 25) to a settings-only role. That is the free list the Architect's blank-query rule exists to prevent. (The Tester flagged it as "same as the GDPR search", but there the role is `data_management`, which may see the full list anyway.) | **fixed**: `likeLiteral` replacer + `ESCAPE '\'`. TDD: `TestSearchCustomersForPicker_WildcardsAreLiteral` fails on the Dev's SQL (`"%" matched as a wildcard: [c-a c-b]`) and passes with the fix (reviewer reverted, ran, restored, ran). A handler-level assertion was added to `TestPromoCustomerPicker_SettingsOnlyRole` (`q=%25`, `q=_` → `[]`). |
| 2 | low | `demo_mode.go`: the new key was placed between `/api/data/customers` and `/api/data/obsolete-items`, breaking the block's alphabetical order. | **fixed**: moved after `GET /api/entitlement`. Surface hash refreshed with `update-docs-shots-surface-hash.sh` (a map-order change, no pixel change). |
| 3 | info | Demo map gofmt realign: the reviewer compared the sorted `key:value` sets from `HEAD` and the working tree. 273 → 274 entries, and the only difference is `+ "GET /api/promotions/customers": true`. No entry was lost or changed. | verified |
| 4 | info | docs-shots manifest: only `surface_sha256`, `promotions.en` and `users.en` moved. The topic hashes equal `sha256sum` of the edited markdown. The fa/ar/tr hashes and all PNGs are untouched. | verified |
| 5 | info | A one- or two-letter name query still returns up to 25 matches, which is how a type-ahead picker works. With the wildcard fixed there is no way to get an unfiltered list, and email is neither returned nor searchable (AC5 + `_NoEmailMatch`). Phone is returned in full (BA Q2 default). | accepted |
| 6 | info | The deny message reads "Manager or admin required" even for a custom role. This is the same key the Promotions page itself renders, and no new string was added. | accepted |
| 7 | info | A manager can no longer find a customer by email in the promotion dialog (BA Q1). The help text says "name or phone number", which is accurate. | accepted (by design) |

Checked with no problems found: the gate matches `requireManager` (`settings`, with the UT_AUTH=off escape hatch via `canPerform`). Anonymous requests get a 401 from the auth middleware (Tester). The route is not under `/self-order`, so the kiosk guard does not apply. Raw SQL stays in `internal/data`. JSON is snake_case and uses the envelope. No new i18n keys. The `esc()` escaping in the result buttons is kept. No file writes or paths are involved, so there is no `MkdirAll` / `paths.Data` issue. Test data uses `example.com` and no real shop names.

## Verification

- Tests (`promotions_customer_picker_5589_test.go`, `search_fold_test.go`)
  use real role grants: custom roles `c_5589settings` / `c_5589datamgmt`
  inserted into `roles` + `role_permissions`, not UT_AUTH=off. They cover
  AC1–AC7 and the empty-array shape.
- Tester mutation runs M1–M6 all go red. Gate → `data_management`, `OR email
  LIKE`, an `Email` field, the demo allow-list entry dropped, and the old URL
  are all caught by Go tests, and `c.ID` casing by the e2e. Reviewer
  re-verified finding 1's TDD as above.
- e2e (Tester): `promotions-record-dialog-3661` + `phone-promotions-3653`, 25
  passed, 0 skipped.
- Reviewer gates after fixes: `gofmt -l .` clean; `go build ./...` ok;
  `go test ./internal/pages/... ./internal/data/... -timeout 20m` ok;
  `golangci-lint run ./...` 0 issues; guards ok: data-access, i18n,
  i18n-meta, help-topics, docs-shots, fixed-dates, mail-addresses,
  core-neutral, help-drift, help-breadcrumbs, help-terms, compliance-claims,
  competitor-naming, kiosk-engine. Tester: `shellcheck scripts/ci/*.sh` 0
  issues. The full `go test ./...` is green with CI's `-timeout 20m`
  (`internal/plugins` needs more than the 10m default on a 4-CPU box).

**Tester visual attestation (quoted):** "I looked at the New-promotion dialog
with picker results at 1280x800, light theme, en, as a settings-only
custom-role operator. Labels sit above their fields, the result button is under
the field, and nothing is clipped or overlapping. I did NOT look at: dark theme
(a `colorScheme: dark` context still rendered light, because the app theme is set by
the app), 10-inch/kiosk or phone sizes, RTL (fa/ar) or German, all by hand.
Those layouts are covered by the 3661/3653 e2e geometry assertions (fa
1024x600, de, 360x740, 481px), which passed. This card makes no visible UI
change." In the driven run (auth ON) the settings-only operator got one
`200 /api/promotions/customers` call with no email in the body. The result
button read "Name · 555-0100", and picking it set the hidden `customer_id`.

Pre-flight: help topic updated (`promotions.md`, `users.md`) + docs-shots manifest updated (topic hashes + surface hash; no PNG change, no visible UI change); `reference/` n/a (ut-docs `roles-and-permissions.md` defers per-permission detail to `users.md`); README n/a (no claim about the picker went stale); en.json/meta n/a (no new keys); ADR n/a (a dialog helper takes its dialog's gate, which needs no new mechanism, and ADR-0087 erase step-up is untouched); review record this file.

**Verdict:** approve (with fixes 1–2 applied).

## Deferred

- Existing debt, out of scope (BA non-goal): `/api/data/customers`
  (`SearchCustomers`, data_management-gated) has the same unescaped LIKE and
  PascalCase `CustomerSummary` JSON. It has no privilege impact, because that
  role may list customers anyway. Candidate Backlog card.
