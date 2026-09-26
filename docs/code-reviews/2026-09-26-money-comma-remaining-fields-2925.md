# Review — money inputs: decimal comma on the remaining fields (ut-docs#2925)

**Branch:** `fix/2925-money-comma-remaining-fields` · **Built by:** Opus 5.5 (lane:cloud-54) · **Reviewed by:** Fable (independent subagent)

## What shipped
- Every remaining money input moved from the dot-only `{{ moneypattern }}` to `{{ moneypatternlocal }}` (comma-tolerant pattern plus `data-money-local`, so the shop-language message from #2819 is shown): promotions `value_amount` ×2, settings payments-fee `fixed`, sale-screen tender `amount`/`change`, voucher `amount`, shifts opening/closing/skim/adjust, menu pfand, reports tips, yüzde usulü ×2, inventory `#stock-cost`.
- `httpx.MoneyPatternLocalAttr(decimals, signed bool)` now mirrors `MoneyPatternAttr`. The signed form is used by the shift adjustment. Existing callers pass `false`.
- Server-parsed fields use `httpx.ParseMoneyMajor` instead of `strconv.ParseFloat`: promotions (create and edit share `parsePromotionForm`) and the payments-fee `fixed` amount. A malformed fixed fee (`abc`, `1e3`, `0,305`) is now refused with the existing `✗ range` instead of being stored as 0. Empty still means 0.
- inventory stock cost is now gated on `utCurrency.parseMinor` instead of `Number(raw)`. `Number` refused "3,50", so the cost was silently dropped, and it accepted "1e3".
- `httpx.MinorFromMajor` removed. It had no callers left (the deadcode guard caught it).
- Help: `promotions` and `reports` (Cash adjustments) topics in en/de/tr/fa/ar say a dot or a comma works. The docs-shots manifest topic hashes were regenerated with `make docs-shots`.
- `TestMoneyTemplates_NoDotOnlyMoneyPattern` fails if any template reintroduces the dot-only `{{ moneypattern }}`.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Blocking (CI) | `manifest.json` topic hashes were stale after the help prose edits, so `guard-docs-shots` would fail. | **Fixed:** `make docs-shots` rerun and the manifest committed. The 124 regenerated PNGs differed only by rendering drift in this container (the UI change is attribute-only: pattern/data attributes, no pixel change), so they were restored rather than committed as churn. |
| 2 | Info | Direct API posts of `5.`, `.5`, `+5`, `NaN` to promotions are now refused. | Accepted: the UI pattern already refused them. Same grammar as #2819. |
| 3 | Info | `money.FromMinor(minor).Minor()` in promotions is a no-op round-trip. | Kept: it marks the money boundary conversion, as in the surrounding code. |
| 4 | Out of scope | `value_percent`, payments-fee `percent` and similar percentage inputs are still dot-only. | Follow-up Backlog card filed. |

The reviewer traced each field's parser end to end and found no defects: tender forms use `FormData` → `toMinor`; shift/tip/pfand/yüzde fields send only the hidden `*-minor` values; the settings elevation re-post re-parses the raw `fixed` value. `data-money-local` custom validity works on every affected form (each submits via a button click or `FormData`).

## Verified
- TDD: the new Go tests failed on pre-fix code with the real errors (`3,50` → `value_invalid`, stored `Fixed:0`, 15 template hits). The e2e spec `e2e/tests/money-comma-2925.spec.ts`: 5/5 failed with the fix stashed and 5/5 passed with it restored. The neighbouring specs (#2819, #1282, #1272 shifts/tips OSK) pass.
- e2e reads values back from the server or from the round-trip response: promotion `3,50` is re-rendered as `3.50`. A shift opened at `3,50`, adjusted by `-3,50`, closed at `3,50` with a `1,00` skim, and each call returned 200. A `7,50` tip was posted as 750.
- Full gate: `gofmt -l .` empty, `go build ./...`, `go test ./...` pass, `golangci-lint run ./...` 0 issues. CI `build` guards pass locally, except the environment-only failures shellcheck-version (no binary) and the deadcode desktop root (`logging.Stderr`, no GTK headers; unrelated and pre-existing).
- UX: attribute-only change, same `inputmode="decimal"`, no layout, string or RTL change. The de/tr OSK comma key now produces an accepted value. Not checked on real touch hardware.

## Verdict
Safe to merge.
