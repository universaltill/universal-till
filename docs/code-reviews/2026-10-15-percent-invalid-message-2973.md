# Review — percent fields get their own invalid message (ut-docs#2973)

**What shipped:** percent inputs (`{{ percentpatternlocal }}`: promotion
percent, payment-fee percent, country tax rate, tax-code rates) showed the
money message `common.money.invalid` ("Enter the **amount** … for example
3.50 or 3,50") on an unparseable value. `<body data-percent-invalid>` in
`web/ui/layouts/base.html` now renders a new key `common.percent.invalid`
("Enter the percentage in digits, with a dot or a comma before up to two
decimals (for example 7.5 or 7,25).") in en/ar/fa/tr. `app.js` already
reads `data-percent-invalid` for `data-money-local="percent"` fields, so no
JS change. The de/es/pt packs get the key in follow-up PRs in the same cycle
(branch `i18n/2973-percent-invalid` in each; `lang-pack-drift` on `main` is
red until they merge — expected, per CLAUDE.md).

**Tests:** `TestPercentInvalidMessage_IsPercentSpecific` (new) — the en text,
the body attribute carrying it, and that it differs from
`data-money-invalid`; failed before the fix (`T` returned the key name),
passes after. `TestPercentInvalidMessage_IgnoresCurrencyDecimals` now expects
the percent key. The e2e spec `percent-comma-2954` also asserts the percent
message is not the money message.

**Review:** independent review on a different model from the author (author:
Sonnet; reviewer: Opus 5.5, fresh context, own worktree). Verdict: safe to
merge, no blockers.
- *Should-fix (fixed):* the pattern `[0-9]+([.,][0-9]{1,2})?` accepts at most
  two decimals, but the first wording didn't say so — an operator typing a
  3-decimal tax rate (8.875) would be told to do what they just did. The
  message now says "up to two decimals" in all 7 locales (core + packs).
- *Nit (accepted):* `IgnoresCurrencyDecimals` compares against `T(...)` of
  the same key, so on its own it can't catch a missing en key; the new test
  and `guard-i18n.sh` both do.
- *Nit (accepted):* under `?lang=de` the e2e run reads English fallback text
  until the de pack PR lands; the not-equal-to-money assertion still holds.
- Coverage check: all 5 templates rendering `percentpatternlocal`
  (promotions, settings, country_settings, tax_codes, plugin_settings) render
  inside `base.html`; standalone pages with their own `<body>` have no
  percent fields.
- Translations ar/fa/tr checked against each file's own `common.money.invalid`.
- TDD claims re-verified by the reviewer: reverting `base.html` fails both
  tests; removing the en key fails the new one; restoring passes.

**Verified beyond unit tests:** Playwright `percent-comma-2954` +
`money-input-2819` against real tills (7/7 pass). `go build ./...`, `gofmt`,
`go test ./internal/pages/` (whole package), every `ci.yml` build-job guard
(all pass; `guard-deadcode-baseline` and `guard-shellcheck-version` could not
run in this container — deadcode binary built with go1.26 vs go1.27 module,
no shellcheck — CI runs both). Visual: the message appears in the browser's
native validation bubble; I did not screenshot it (text-only change, no
layout touched).

**Deferred:** the help pages for tax codes and country settings don't mention
the two-decimal limit either (pre-existing; low).
