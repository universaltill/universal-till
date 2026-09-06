# Code review: number/date formatting follows store.locale

- **Card:** universaltill/ut-docs#1130
- **PR:** universaltill/universal-till (branch `feat/1130-locale-number-date-format`)
- **Complexity:** medium — built at Sonnet, reviewed at Opus (independent subagent, isolated worktree)
- **Date:** 2026-09-06

## What was wrong

`internal/httpx/currency.go`'s existing `LocalizeDigits`/`localeDigits`
mechanism only substitutes digit *glyphs* (Persian/Arabic numerals) for
`fa`/`ur`/`ps`/`ar`. Nothing derived thousands/decimal-separator convention
(German `1.234,56` vs. English `1,234.56`) or date ordering (`DD.MM.YYYY`
vs. `MM/DD/YYYY`) from `store.locale` — every Latin-digit locale rendered
money and dates in the hardcoded English convention regardless of the
shop's configured locale.

## What changed

- **Mechanism** (server-side Go, per ADR-0008's server-rendered-HTMX
  model — no client-side `Intl.*`): `numberSeparators(locale)` and
  `dateLayout(locale)`, table-driven by locale base language, the same
  per-locale-family pattern `localeDigits` already used. Grouping is
  applied *before* `LocalizeDigits`' digit-shape substitution, so the two
  compose without either one needing to know about the other.
- `FormatMoney`/`FormatQty` gain `*Latin` siblings (`FormatMoneyLatin`,
  `FormatQtyLatin`) — same grouping/decimal convention, but always Latin
  digits, for ESC/POS print paths that can't render non-Latin numeral
  glyphs in text mode (bitmap mode would be needed for that — out of
  scope). Same split for dates: `FormatDate`/`FormatDateLatin`.
- New `"date"` template func in `httpx.FuncsFor`, locale-bound like
  `"money"` already was, used by `invoice.html`'s single-invoice display.
- `buildReceiptDoc` (`print_api.go`), `buildEODDoc` (`eod_api.go`),
  `buildInvoiceDoc`'s Meta line (`invoice_page.go`), the shelf-label print
  handler, and the kitchen ticket's Qty (`kitchen_print.go`) all now
  follow the shop's configured locale (`httpx.DefaultLocale()`) for
  grouping/decimal/date-order convention instead of a hardcoded `"en"`,
  while keeping digit shape forced Latin where the print path requires it.
- `invoices.html` (register list) and `invoice.html` (single document) now
  format `IssuedAt` instead of showing the raw stored RFC3339 string.
- Client-side money formatting (`web/public/app.js`'s `window.utCurrency`)
  now reads `data-number-thousands`/`data-number-decimal` attributes
  (`base.html`, sourced from the same `numberSeparators` table via new
  `"thousandssep"`/`"decimalsep"` template funcs) instead of hardcoding
  `,`/`.` — keeps server-rendered and client-rendered money agreeing on
  the same screen (e.g. the payment overlay next to the basket total).
- Tests: `internal/httpx/currency_test.go`, `dateformat_test.go`,
  `httpx_test.go` — locale-grouping/date-order cases across every one of
  this product's shipped country defaults, the Latin-digit-forcing
  variants, and a regression check that the pre-existing fa/ur/ps/ar digit
  substitution is unaffected.

## What the independent review found (Opus, isolated worktree)

**One blocker, fixed in this same PR before commit:** the first draft's
`numberSeparators`/`dateLayout` tables had exactly one non-default entry
(`de`) each. But `internal/data.BuiltinCountryDefaults` unconditionally
presets `store.locale` for GB/US/DE/**FR/ES/IT/NL/TR** at setup
(`country_settings_page.go`'s own comment confirms this), and `tr` is a
**bundled** UI locale (`web/locales/tr.json` ships in-tree; `de` doesn't —
it's the external `ut-plugin-language-de` pack). So `fr-FR`/`es-ES`/
`it-IT`/`nl-NL`/`tr` shops — a bundled locale among them — still got the
wrong grouping/date order, and the first draft's own tests **asserted the
wrong `tr` answer as correct**, which would have broken a later, correct
fix and read as a regression. Reproduced by the reviewer via a real
`go test -overlay` run showing `FormatMoney(123456, "de-DE")` giving
`"€1,234.56"` (wrong) against the pre-fix logic. Fixed by extending both
tables (`es`/`it`/`nl`/`tr` join `de`'s period-thousands/comma-decimal
family; `fr` gets its own space-thousands/comma-decimal convention; `nl`
gets its own hyphen-separated date; `tr` joins `de`'s dot-separated date)
and correcting the test expectations.

**Six should-fix findings, all fixed in this same PR:**
- `buildEODDoc` (Z-report print) and the shelf-label print handler still
  hardcoded `"en"` — same anti-pattern the diff had already removed from
  `buildReceiptDoc` one function away. Fixed to use `httpx.DefaultLocale()`
  + the `*Latin` variants, consistent with the receipt path.
- The printed invoice document (`buildInvoiceDoc`'s Meta line) and the
  single-invoice HTML page (`invoice.html`) still showed the raw
  `IssuedAt` string — the diff had formatted the register *list* but not
  the document a customer/accountant actually receives, despite both
  routes being claimed by the same help topic. Fixed both.
- The invoices-list date format change silently flipped `IssuedAt` from
  UTC to Local, disagreeing with `invoice_page.go`'s own documented reason
  for comparing `from`/`to` against the raw UTC string (a local-vs-UTC
  calendar-day skew the code explicitly reasons about elsewhere). Fixed
  by adding a `"dateUTC"` template func (no `Local()` conversion) and
  using it on the register list specifically, leaving the general-purpose
  `"date"` func's Local conversion — correct for a wall-clock event like a
  receipt or the single-invoice page — unchanged.
- The client-side money formatter (`app.js`) still hardcoded `,`/`.`,
  so a `de` shop showed `€1.234,56` server-rendered next to `€1,234.56`
  client-rendered (payment overlay pills) on the same screen — squarely
  in the AC's "operator UI". Fixed by threading the same
  `numberSeparators` table through two new data attributes.
- The `"date"` template func degraded an unparseable string to `""`
  rather than the original value — several stored date strings in this
  codebase are `"2006-01-02"`-shaped, not RFC3339, so a future
  `{{ date .Day }}` caller would get a silently blank cell. Fixed to
  return the input unchanged on parse failure.
- The comment on `kitchen_print.go`'s `Qty` line claimed Latin
  "digits/separators" when only digit shape is forced Latin — separators
  do follow locale there, same as the receipt. Wording fixed.

**Latent, not-yet-real risk documented (not fixed — genuinely no locale
triggers it today):** `LocalizeDigits` swaps `,`/`.` to Arabic separator
glyphs by raw byte value, assuming `,`=thousands/`.`=decimal. None of the
digit-substituted locales (`fa`/`ur`/`ps`/`ar`) are in `numberSeparators`'
non-default families, so nothing mis-swaps today — but the day a
digit-substituted locale needs a non-default separator pair, the two
glyphs would silently swap roles. Documented directly in
`numberSeparators`' doc comment as a flag for whoever adds that locale,
rather than building a role-aware substitution mechanism now for a case
that doesn't exist yet.

**Explicitly out of scope for this card, left as follow-up:** shift-close/
EOD-report pages beyond the Z-report print (the HTML side uses
`<input type="date">`, browser-native, nothing server-rendered to
localize), journal, audit log, and the my-reports bug-report list all
still render raw stored date strings in some cases — mechanical `{{ date }}`
substitutions once this mechanism existed, not new design work.
`en-IN`/`ur-PK` lakh/crore grouping (a materially different grouping rule,
not just a different separator) and thousands-grouping on already-small
print quantities are both pre-existing/low-frequency and belong in a
follow-up rather than this card. A Backlog card should be filed for the
follow-up date-format sweep; not filed as part of this cycle to keep this
close-out from ballooning — noting it here as the record of the decision.

## Verification (beyond the reviewer's independent pass)

- `gofmt -l .`, `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` (0 issues) — clean, after every fix round.
- `go test ./...` (full suite) — green, twice (once pre-review-fixes, once
  post).
- **TDD re-verification, mine:** reverted `internal/httpx/currency.go` and
  deleted `internal/httpx/dateformat.go` back to `origin/main`'s state,
  confirmed the new tests fail at build time (`undefined: FormatMoneyLatin`
  etc.), restored, confirmed green again.
- **TDD re-verification, the reviewer's (independent, sharper — assertion-
  level not just build-level):** `go test -overlay` swapping in `origin/main`'s
  grouping/date logic while keeping the new symbols compiling, confirmed
  `TestFormatMoney_LocaleGrouping`/`TestFormatQty`/`TestFormatDate`/
  `TestFormatDateLatin` fail with the exact wrong-value diagnostics, restored,
  confirmed green.
- **New test for the reviewer's own UTC-vs-Local finding:** added
  `TestFuncsForExposesDate`, which temporarily moves `time.Local` to a
  fixed UTC+3 zone and asserts `date`/`dateUTC` disagree across a day
  boundary exactly as the fix intends — a UTC-only test runner couldn't
  otherwise distinguish the two.
- 17 CI-blocking guards from `.github/workflows/ci.yml`'s `build` job, run
  directly: `guard-data-access.sh`, `guard-i18n.sh`,
  `guard-page-http-error.sh`, `guard-compliance-claims.sh`,
  `guard-docs-shots.sh` (regenerated via `make docs-shots` after every
  template/handler change — `tr` locale screenshots genuinely changed,
  showing period/comma grouping instead of comma/period, confirming the
  fix visually, not just in unit tests), `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`,
  `guard-help-topics.sh`, `guard-makefile-version.sh`,
  `guard-kiosk-engine.sh`, `check-brand-assets.sh` — all pass.
- Manual (`web/help/`) check: no page's steps or flow changed (a display-
  formatting refinement, not a new screen or step), and no existing help
  topic documents an exact punctuation convention that went stale —
  confirmed by reading `web/help/en/invoices.md`. Screenshots regenerated
  where the rendered page's numbers/dates actually differ.
- No real shop/client name, no secret-shaped literal, in the diff.

## Safe-to-merge verdict

Safe to merge. The blocking defect (F1: incomplete locale coverage,
including a wrong answer locked into the test suite) and all six
should-fix findings from the independent review were fixed and
re-verified in this same PR; the remaining nits are documented, not
blocking.
