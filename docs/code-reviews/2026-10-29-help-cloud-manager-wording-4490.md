# Review — help: call my. by its name, not "the shop's cloud manager" (ut-docs#4490)

Date: 2026-10-29 · Branch: `fix/4490-help-cloud-manager-wording` · Lane: `lane:cloud-41`

## What shipped

"Cloud manager" is now the named Shop-tier feature (ADR-0162), so the manual's
generic "the shop's cloud manager" suggested every plan has it. Prose only:

- `web/help/en/sell.md` — "in the shop's cloud manager" → "on my.universaltill.com";
  the same sentence in `de`, `ar`, `fa`, `tr` `sell.md` now names my.universaltill.com.
- `web/help/en/multitill.md` — "The Tills page of the shop's cloud manager" →
  "The Tills page on my.universaltill.com" (the translations don't carry that
  sentence yet; already recorded drift, unchanged).

No other "Cloud manager" wording remains in `web/help/**`.

## Review

Independent review by a different model (Sonnet; the change was authored by Opus).

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | — | de/ar/fa/tr replacements grammatical and natural; meaning kept | — |
| 2 | minor | "the shop's cloud manage page" (en claim/display/catalog/categories + de/tr/fa/ar) is the same defect class, weaker (lower-case, descriptive) | Deferred: follow-up Backlog card — each locale needs grammar-aware rewording (Turkish case suffixes), outside the two lines this card names |
| 3 | — | "Tills page on my." is accurate (`manage_app.node.tills`, `manage_app.till.problems_reported` in ut-cloud locales; multitill.md already points at my.'s Tills page) | — |

## Verified

- `guard-help-drift.sh`, `guard-help-topics.sh`, `guard-help-breadcrumbs.sh`,
  `guard-compliance-claims.sh`, `guard-competitor-naming.sh`,
  `guard-mail-addresses.sh`, `guard-i18n.sh` and the rest of `ci.yml`'s build-job
  guards pass locally (except `guard-deadcode-baseline.sh` / `guard-shellcheck-version.sh`,
  which fail on this container's tool versions only — CI runs them).
- `go test` for `internal/httpx`, `internal/manual`, `internal/pages`,
  `internal/pages/catalog`, `internal/pages/common`: ok. `internal/plugins` left to CI
  (exceeds the local 10-minute default).
- No screenshots affected (prose only).

Verdict: safe to merge.
