# Review: plan names are product names in every locale (ut-docs#4738)

**Date:** 2026-10-21 · **Lane:** lane:cloud-41 · **Built by:** Opus 5.5 (inline, complexity:easy) · **Reviewed by:** Sonnet 5.5 (independent subagent)

## What shipped
ADR-0060 amendment 2026-10-17 point 2: plan names are product names, the
same in every language. ut-cloud already shows "Local", "Local Plus",
"Shop", "Pro" and "Multi-location" untranslated in every locale.

- `web/locales/ar.json`, `fa.json`, `tr.json`: `subscription.plan.shop` /
  `pro` → "Shop" / "Pro". `subscription.plan.local` → "Local (<free>)",
  with only "(free)" translated. `subscription.free_note` names the plan
  "Local".
- New `web/locales/plan_names_test.go` (`TestPlanNamesAreNotTranslated`):
  the shop, pro and chain labels must equal en.json, and the local label
  must start with "Local". It failed on the old ar/fa/tr values (8
  errors) and passes now.
- The packs ship in their own PRs: de/es/pt `subscription.plan.chain`
  "Chain" → "Multi-location". In de, `plan.local` and `free_note` now say
  "Local". Each pack's manifest gets a patch bump.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `subscription.free_note` still translated "Local" (ar/fa/tr/de) | **Fixed** |
| 2 | nit | Mixed-direction "Local (مجانية)" in RTL could reorder if concatenated | Accepted. It renders correctly in `<strong>` and in `renew_hint`. |
| 3 | minor | The packs have no equivalent guard; the test hard-codes 4 keys | Accepted for now. Follow-up card filed for a pack-side check. |

## Verified
- `go test ./web/locales/`: red on the old values and green on the new ones.
- `go test ./...` and the ci.yml build-job guards pass. Two guards fail in
  this container for tooling reasons and fail the same way on `main`:
  deadcode needs go1.27, and shellcheck is absent.
- Each pack: `scripts/validate.sh` passes. `check-key-drift.sh` against
  this branch's en.json reports 0 drift.

**Verdict:** safe to merge.
