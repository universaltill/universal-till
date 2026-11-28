<!-- Filename convention in universal-till/docs/code-reviews/: YYYY-MM-DD-<topic>.md. Land as 2026-11-28-settings-templates-close-6332.md -->
# Review record: settings templates close-out (ut-docs#6332, cards 6346 6347 6348 6351 6352 6353)

Reviewer: Fable independent subagent plus orchestrator read of the Go diff. Pre-flight: walked.

## Change
ApplyPerTill (satellite seeds only per-till theme + marker), ExplicitClasses + generated docs/shop-template-classes.json, registrydesc.CardForRegion, template-rank test, e2e matrix/cashier specs, help topic choose-shop-type (+ screenshots).

## Findings
1. Medium, accepted as follow-up card: internal/pages/shoptemplate_apply.go:55-62. ApplyPerTill is reached only from the first-run wizard (setup_page.go:78). A till that joins a main later never seeds its theme (design section 3 point 4, AC4.2). TestApplyShopTemplateSatelliteSeedsOnlyPerTillTheme calls the function directly so it does not prove the wiring. Dormant: no shipped template seeds `theme`.
2. Nit: cmd/gen-settings-registry/main.go:45 classes write has no own MkdirAll and is cwd-relative; safe because the registry write creates docs/ first and the go.mod guard runs.
3. Nit: apply_test.go:192 sale.sensitive case is refused by the whitelist, not the tier check; classes_test.go:72 ignores the Unmarshal error and its comment claims a kiosk check it does not make.

## Verification
Generator output byte-identical to committed docs/shop-template-classes.json and registry; vet and targeted tests pass. Seed deny-prefix runs before whitelist; Apply/ApplyPerTill re-validate; satellite never writes ledger or shop-wide keys nor over a set value; cashier spec asserts 403 and unchanged type. English-only i18n. Playwright specs not run by reviewer.

## Verdict
MERGE OK; open follow-up card for finding 1.
