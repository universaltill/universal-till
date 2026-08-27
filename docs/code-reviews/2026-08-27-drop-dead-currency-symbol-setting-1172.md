# Code review: drop the dead currency_symbol setting (ut-docs#1172)

**Date:** 2026-08-27
**Author:** pipeline (BA/Architect/Dev/Tester at Sonnet, complexity:easy)
**Reviewer:** independent fresh-context Sonnet subagent, isolated git worktree
**Branch:** fix/1172-drop-dead-currency-symbol-setting

## What shipped

A real, user-reported bug on the live Pi 5 till: after a German wizard run,
`store.currency`='EUR' (correct) but `store.currency_symbol`='£' (stale, GB
boot default). The report worried this meant receipts print the wrong
symbol on a EUR shop.

Investigation showed the actual risk described in the report does not
exist: `store.currency_symbol` / `config.Locales.CurrencySymbol` is a
**dead, drift-prone setting** — written once at boot as a load-then-
immediately-resave round-trip (`internal/app/app.go`'s
`settingsStore.LoadRuntimeConfig` → `SaveRuntimeConfig`) and read by
nothing else anywhere in the codebase. Every real, live symbol display —
the `{{ money }}` template func, `httpx.ActiveCurrency()`, receipts
included (print code has no currency-symbol logic of its own) — already
derives the symbol from `store.currency` (the currency *code*) alone, via
the hardcoded registry in `internal/httpx/currency.go`. The wizard sets
`store.currency` correctly; it never touched the separate dead setting,
which is why the DB showed the drift the reporter saw.

Rather than patch the wizard to also populate a second, still-independently
-driftable symbol setting, this deletes the dead field entirely —
consolidating on the single already-correct, currency-code-driven
derivation path used everywhere else:

- `internal/config/config.go`: removed `Locales.CurrencySymbol` and its
  `UT_CURRENCY_SYMBOL` env default; left an explanatory comment.
- `internal/settings/runtime.go`: removed the `store.currency_symbol`
  get (`LoadRuntimeConfig`) and set (`SaveRuntimeConfig`); doc comments
  updated from "seven keys"/"7 keys" to "six keys"/"6 keys".
- `internal/config/config_test.go` / `internal/settings/runtime_test.go`:
  updated/removed assertions referencing the removed field, including
  deleting `TestLoadRuntimeConfig_CurrencySymbolIndependentOfCurrency`
  (its subject no longer exists) and adding a negative assertion in
  `TestSaveRuntimeConfig_WritesExpectedKeys` that `store.currency_symbol`
  must never be persisted (TDD: confirmed RED against the pre-fix code,
  GREEN after).
- `internal/pages/setup_page_test.go`: new end-to-end regression test,
  `TestSetupWizardDrivesDisplayedSymbolFromCurrencyCodeAlone`, posting a
  real DE/EUR wizard commit through `/api/setup` and asserting
  `httpx.ActiveCurrency().Display == "€"` plus that `store.currency_symbol`
  is never written — proving the receipt-facing behaviour the original
  report worried about is actually correct.

Not touched, deliberately: `data.CountrySetting.CurrencySymbol` and its
call sites (`country_settings_repo.go`, `country_settings_page.go`,
`web/ui/pages/country_settings.html`) — a different, legitimate field (the
per-country catalog default used to seed wizard prefills and the Settings
admin page), unrelated to the dead shop-level setting removed here. No UI
surface, no i18n key, no money-type, no file I/O, no DB migration (generic
KV settings table — a stale leftover row is inert once nothing reads it).

## Independent review

Fresh-context Sonnet subagent, isolated git worktree, no visibility into
the implementer's reasoning. Independently re-derived the dead-field claim
from the code rather than taking the diff's comments on faith.

**Commands run, all PASS:** `gofmt -l` (changed files), `go build ./...`,
`go vet ./...`, `go test ./internal/config/... ./internal/settings/...
./internal/pages/...`, full `go test ./...` (41 packages, all `ok`),
`scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh`.

**Premise independently verified**: confirmed `app.go`'s load-then-resave
round-trip is the only writer/reader of the field outside the diff itself;
repo-wide search for `Locales.CurrencySymbol` post-diff returns zero hits;
repo-wide search for `store.currency_symbol` post-diff returns only the
reviewed code/tests plus two immutable historical `docs/code-reviews/`
records (correctly left alone); confirmed all 6 `httpx.InitCurrency(...)`
call sites pass a currency code, never a symbol; confirmed `{{ money }}` →
`FormatMoney` → `ActiveCurrency()` → the code-keyed registry is what
actually renders every money amount, receipts included.

**TDD claims independently re-verified**, not taken on faith, both by
mutation:
- `TestSaveRuntimeConfig_WritesExpectedKeys`: re-added the
  `store.currency_symbol` write to `SaveRuntimeConfig` → test failed with
  the exact claimed error (`"SaveRuntimeConfig wrote store.currency_symbol
  — this setting is dead and must not be persisted"`); reverted → passes.
- `TestSetupWizardDrivesDisplayedSymbolFromCurrencyCodeAlone`: commented
  out `httpx.InitCurrency(st.Currency)` in the wizard handler → test failed
  with `Display = "£"` instead of `"€"`; reverted → passes.

**Scope check:** diff touches exactly the 5 files listed above; the
unrelated `country_settings` catalog code (repo, admin page, template) is
confirmed untouched. No money.Money usage touched. No template/locale file
touched — consistent with `guard-i18n.sh` passing and no new user-facing
string. No file-write bug-class risk (no file I/O in this diff at all).

**Code quality:** the new `setup_page_test.go` test matches the file's
existing conventions (same `newFullAuthDeps`/`postForm` helpers, same
`t.Cleanup`-for-process-global-state pattern already used by
`import_currency_confirm_test.go`/`setup_tse_test.go`); `Locales.Currency`
doc comments are accurate.

## Findings

One cosmetic nit, fixed: the explanatory comment on the removed field was
placed after the struct's pre-existing "add more fields as needed" line;
reordered so the removal note reads before it. No other findings —
blocker, high, or low.

## Verified beyond automated tests

- Reviewer's own revert-then-restore mutation testing on both regression
  tests (above), confirming each fails for the *right* reason, not an
  unrelated one.
- Repo-wide greps for every remaining reference to the removed field/key,
  confirming nothing outside this diff and immutable history still
  depends on it.
- No visible UI surface changed, so no screenshot/manual update applies —
  confirmed by inspecting the diff's file list (no `.html`/`web/locales`
  files touched).

## Safe-to-merge verdict

**Yes.** No blockers, no should-fix items beyond the one cosmetic nit
(fixed). No deferred follow-up beyond this ticket's own scope — the
"audit other derived settings for the same skew" acceptance item was
checked during BA/Architect: region/locale/tax_inclusive/tax_rate all
round-trip correctly through `RuntimeState`; no sibling orphaned field
found.
