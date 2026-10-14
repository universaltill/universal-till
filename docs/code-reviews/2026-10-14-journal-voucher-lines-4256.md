# Code review — journal receipt shows gift vouchers (ut-docs#4256)

**Date:** 2026-10-14 · **Branch:** `fix/4256-journal-voucher-lines` · **Author:** Opus 5.5 (lane `cloud-41`) · **Reviewer:** Fable (independent subagent, one round)

## What shipped

`/journal/{receipt}` showed nothing for gift vouchers. A voucher-only sale had
no item line, Subtotal 0.00 against Total 25.00, and no voucher number. A
payment by voucher read only "voucher". `data.SaleDetail` already carried the
data (`VoucherIssues`, `VoucherIssueTotal`, payment `VoucherID`), so the fix
is in the page:

- Every issued voucher is its own line: "Gift voucher", its number, the
  holder ("For …"), and *Any purchase* / *Specific item (taxed now)*. A voided
  voucher is marked with the printed receipt's `receipt.vouchers.void` text
  (#4224), read through `VouchersIssuedInSale`.
- A **Vouchers issued** totals row shows `VoucherIssueTotal` when it is
  non-zero. Any-purchase vouchers are in Total but not in Subtotal (a
  liability), so the totals now add up. A single-purpose voucher is already in
  the subtotal and adds no row.
- A voucher payment shows the redeemed voucher's number.
- For anyone who can open the settings-gated `/vouchers/{id}`, the number
  links there (`url.PathEscape`d in Go). Others see the number only.
- Manual: a paragraph in `reports.md` (the topic that owns `/journal/{receipt}`)
  in en/de/tr/fa/ar. Its bold labels match the shipped UI strings, de taken
  from `ut-plugin-language-de`.
- No new locale keys, so no language-pack follow-ups.

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `href="/vouchers/{{ .VoucherID }}"`: html/template leaves `/ ? #` alone, so a code such as `GS/7` (valid, used in fixtures) 404s | **Fixed**: hrefs are built with `url.PathEscape` in the handler; the test covers `GS/7?x#y` → `GS%2F7%3Fx%23y` |
| 2 | minor | A voided voucher rendered as an ordinary issue | **Fixed**: marked void, with a test |
| 3 | minor | A 64-char code could overflow the table on phones | **Fixed**: `.pill-code` (`overflow-wrap: anywhere`) |
| 4 | nit | The test did not assert the totals row amount; the mixed fixture skipped tax_total | **Fixed** |
| 5 | nit | Under exclusive pricing, a single-purpose voucher's price cell is the face value while goods show net | Accepted: the face value is what the customer sees on the voucher |
| 6 | nit | tr wording ("tutarı tutar") | **Fixed**; the de "Mehrzweck-Gutschein" prose is kept (correct term) |

## Verification

- TDD: the handler test failed with 11 missing assertions before the fix (re-verified by reverting the template and handler, then restoring). Findings 1 and 2 were red-then-green too.
- `go build ./...`, `go vet`, `gofmt -l`, `golangci-lint` (0 issues), and full `go test ./...` all green. `internal/plugins` timed out once at the 10-min default under CPU contention (an e2e run and the reviewer were running at the same time) and passes alone (841s). The test running at the timeout was in an area this diff does not touch.
- All `ci.yml` guards pass locally except two that this environment cannot run: `guard-deadcode-baseline` (the local tool was built with Go 1.26) and `guard-shellcheck-version` (no shellcheck). No shell changes. CI runs both.
- e2e `gift-voucher-item-4214.spec.ts`, extended: from the voucher's page, both "sale" links open journal receipts. The issuing one shows the line, the purpose and a 25.00 voucher total, and fits at 1024×600 and 360×740. The redeeming one names the voucher on its payment row. 2 passed (also re-run by the reviewer).
- Looked at: screenshots of the issuing receipt at 1024×600 and 360px (en, light). The totals read 0.00 / 0.00 / 25.00 / 25.00. Not driven: fa/ar RTL and dark theme. The template adds no direction-specific CSS (only the existing `text-align:end`).
- Pre-existing and out of scope: at 360px the page header's three buttons push the page to about 518px wide. Filed as ut-docs#4279.

## Verdict

Safe to merge.

## CI follow-up

The first push failed `guard-docs-shots`: the surface and the `reports` topic
changed, so the screenshot manifest was stale. A first `make docs-shots` run
rewrote 116 PNGs because the till's "Storage almost full" chip leaked into
them: this container's volume had less than 10% free, below
`diskspace.Floor`. That run was discarded. Re-run with `TMPDIR=/dev/shm` so
the throwaway till's data dir sits on a roomy tmpfs, every PNG came out
byte-identical and only `manifest.json` changed. The harness's dependence on
host disk space is filed as its own card.
