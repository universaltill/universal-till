# Code review: catalogue import currency-confirmation gate (ut-docs#970)

**Branch:** `fix/970-import-currency-confirm`
**Reviewer:** independent Opus subagent, fresh context (complexity:medium →
Opus review, per scrum-master's model routing) · **Author:** Sonnet (this
pipeline cycle)

## What shipped

A fresh till defaults to GBP with nothing recording whether an operator ever
actually chose that. Importing a catalogue (CSV or a speedy-kasse/pepperm-
cashbox `.bkp` backup) prices every row under the till's active currency, so
importing e.g. a German catalogue into a fresh, never-configured till
silently priced everything as GBP with no warning.

- New settings key `common.KeyCurrencyConfirmed`, set only on a genuine
  explicit currency choice: the Settings currency card, the setup wizard's
  country select (only when the operator actually touched it — see F3
  below), or the import confirmation prompt itself.
- `POST /api/import`: committing while unconfirmed stops before writing
  anything and renders a confirmation prompt instead. Preview is never
  gated. Confirming a different currency re-parses the original upload
  under the new decimal count and switches the till's live currency.
- A backward-compat migration (`063`) backfills `currency_confirmed=true`
  for installs that already finished setup, so already-running installs
  aren't gated on their very next import.
- New locale keys (en/ar/fa/tr) and a new manual step in the catalog topic
  (4 locales).

## Independent review — first round: needs fixes

Full independent pass (different model, fresh context, isolated worktree):
gate control-flow soundness confirmed (every write path — item creation,
stock, tax codes, audit — sits inside `if commit { ... }` after the gate;
`rows` is built from the post-re-parse `res`), the `.bkp`/CSV re-parse
mechanics verified correct (`ParseBkp` is `io.ReaderAt`-based so
position-independent; `Parse`'s `io.Reader` genuinely needs the `Seek(0,
...)`, present and proven), and the `hx-encoding="multipart/form-data"` fix
independently confirmed necessary by reading the vendored htmx source
(`hx-include` on a sibling-of-`<form>` element does not inherit encoding).

**TDD re-verified independently, not taken on trust**: neutered the gate
(`currencyConfirmed = true` hardcoded) and confirmed
`TestImport_UnconfirmedCurrency_BlocksCommitWithoutConfirmation` and
`TestImport_UnconfirmedCurrency_ConfirmDifferentCurrencySwitchesTillAndReparses`
both fail against the neutered code — the first failure's output literally
printed the ut-docs#970 bug (a German-priced row committed as `£5.00`).
Restored and confirmed the full suite green again.

But the first round also found a genuine blocker and several real bugs —
shipped in the diff, not deferred:

## Findings — all fixed in this same round

- **F1 (blocker)**: `confirm_currency` validation was `httpx.CurrencyByCode(v).Code
  == v`, which is **always true** for an already-uppercased/trimmed `v` —
  `CurrencyByCode` fabricates a plausible `CurrencyInfo` for any unknown
  code rather than rejecting it, so the "validation" never rejected
  anything. Confirmed live: `confirm_currency=XYZ` switched the till to a
  currency that isn't in the registry at all and marked it confirmed — the
  exact "money labelled as the wrong currency" class this card exists to
  fix, reopened through the fix's own confirm step. **Fixed**: added
  `httpx.IsKnownCurrency`, a real registry-membership check, used both here
  and (opportunistically, same root cause) in the setup wizard's own
  currency field. Regression test:
  `TestImport_UnconfirmedCurrency_RejectsUnknownConfirmCurrency`.
- **F2**: the currency-confirmed marking was wired to `POST
  /api/settings/upsert`'s generic key/value switch — the advanced raw
  table, not the handler the shipped Settings currency card actually posts
  to (`POST /api/settings/save`). An operator using Settings normally
  stayed gated on their next import. **Fixed**: marked in
  `/api/settings/save`'s currency branch instead (kept the upsert-handler
  marking too — harmless, correct for a caller that does use the raw
  table). Regression test added to `TestDisplayAndStoreSettings`.
- **F3**: the setup wizard marked confirmed on **any** completed run —
  country/currency start pre-filled from OS locale + timezone detection
  (ut-docs#590), not from an operator choice, and the wizard can complete
  without the operator ever opening the country step. In practice this
  meant the gate would rarely fire for a real install, including the
  ticket's own scenario. **Fixed**: added a `currencyTouched` Alpine.js
  flag (`web/ui/pages/setup.html`) that only flips true on a genuine
  `@change` on the country select, submitted as a hidden
  `currency_touched` field; `setup_page.go` only marks confirmed when it's
  `"1"`. Three-case regression test:
  `TestSetupWizardCurrencyConfirmedOnlyWhenOperatorTouchedCountrySelect`.
- **F4**: switching currency from the import prompt performed the same
  destructive-ish switch the Settings card does, but without that card's
  "does not convert existing prices" warning (till isn't necessarily
  empty — the setup wizard's starter catalogue, or a prior import, may
  already hold priced items) and without any audit trail of the switch
  itself (the import's own audit row only ever records the *final*
  currency, never the change). **Fixed**: reused the existing
  `settings.currency.warning` locale key in the prompt, added a dedicated
  `currency_switched_by_import` audit row recording from/to.
- **F5**: `TestImport_UnconfirmedCurrency_PreviewStillWorksButWarns`
  asserted only that the parsed rows rendered, not that the warning
  banner did — it passed unchanged against a build with the warning
  suppressed entirely. **Fixed**: now asserts the warning block and
  currency code are present.
- **F6**: the confirm/warning blocks used `class="row-warn"`, which the
  CSS only styles as `tr.row-warn td` — a non-table block got no
  background/border at all, reading as plain body text with no warning
  treatment. Visible in the implementer's own screenshot. **Fixed**: added
  `.notice-block-warn`, a real block-level style (same amber palette),
  used by both the confirm prompt and the preview warning.
- **F7**: migration file's header comment said `-- 056:` instead of
  `-- 063:` (copy-paste artifact from an earlier, since-renumbered draft).
  **Fixed**.
- **F8**: the prompt's help text said "...then import again", but the
  actual flow is one round trip via "Confirm & Import" — no second import
  needed. **Fixed** in all four locales.
- **F10**: zero `.bkp`-path coverage for the gate, despite a speedy-kasse
  backup being the ticket's own headline scenario (every other gate test
  drove the CSV path only). **Fixed**: added
  `TestImport_UnconfirmedCurrency_BkpPathGatedAndReparses`, including a
  decimals-differ (GBP→IRT) re-parse assertion on the `.bkp` bytes
  specifically.
- **F11**: a `Settings.Get` read failure defaulted `currencyConfirmed` to
  `true` (fail open) — for a money-labelling gate, a read error should
  fail safe (prompt), not silently let an unverified currency through.
  **Fixed**: default flipped to `false`.

Deferred, genuinely out of scope for this card: `internal/pages/
setup_page.go`'s pre-existing (not introduced by this diff) use of the same
permissive `CurrencyByCode` idiom on the wizard's *other* currency-adjacent
reads was tightened where this diff already touched that line, but a
broader audit of every `CurrencyByCode` call site was not attempted.

## Verified beyond automated tests

- Real browser (Playwright, pre-installed Chromium) run of the full flow —
  fresh till, upload → gated → confirm same currency → committed; a second
  run confirming a *different* currency (IRT, 0 decimals vs GBP's 2) —
  proved the till switched, `httpx.ActiveCurrency()` reflected it
  immediately (no restart needed), and the re-parsed price landed under the
  new decimal count.
- Screenshots taken and read (not just asserted on) in both English (LTR)
  and Persian (RTL) before and after the F6 styling fix — layout clean,
  RTL mirroring correct, warning block now visibly amber-bordered.
- `guard-docs-shots.sh` correctly caught a real gap mid-review: the
  companion `#969` PR's own review-round edit to `backoffice_page.go`
  triggered the same app-surface-hash guard and failed CI on push — fixed
  there (by a concurrently-running pipeline lane) and independently
  confirmed here that this branch's own surface hash was regenerated fresh
  after merging that PR's now-`main` state in.

## Safe-to-merge verdict

**Yes**, after the fixes above. All 16 CI-blocking guards pass locally.
`go build`/`go vet`/`gofmt -l .` clean. Full `go test ./...` green. Second
review round not warranted: the first round's blocker (F1) and every other
finding were fixed directly in this same pass, scoped exactly to what was
flagged — not a re-review of the whole diff.
