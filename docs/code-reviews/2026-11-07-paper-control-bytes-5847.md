# Review — strip control bytes from printed free text (ut-docs#5847)

**Date:** 2026-11-07 · **Branch:** `fix/5847-paper-control-bytes` (ut-docs: `docs/5847-paper-control-bytes`)
**Author model:** Sonnet (dev subagent) · **Tester:** Opus 5.5 subagent · **Reviewer model:** Opus 5.5 (independent subagent, separate worktree)

Pre-flight: web/help topic — n/a (no help topic describes the printer charsets or control characters; nothing visible changes for normal text); `reference/` — updated (ut-docs `architecture/receipt-printing.md` "Character sets": new paragraph, `ascii` now "control → space"); README — n/a (no text went stale); en.json/en.meta — n/a (no keys); ADR — n/a (hardening inside the existing encoder, no new decision); plan/price, platform claim, plugin taxonomy/host functions — n/a; review record — this file.

## What shipped
- `internal/print/papertext.go` (new): `paperControl` (Unicode Cc = C0/DEL/C1, U+2028/2029, bidi embeddings/overrides U+202A–202E) and `paperText` (each such rune → one space, each invalid UTF-8 byte → `?`, everything else byte-for-byte; returns `s` itself with no allocation when clean). Copy helpers `paperDoc` / `paperTicket` / `paperSlip` / `paperVoucher` sanitise every text field into freshly allocated slices, so the caller's `Doc` is never mutated.
- `encodeText` calls `paperText` before every charset arm, so all ESC/POS output (receipt, Z/EOD report, kitchen ticket, voucher slip, shelf label) is covered at one choke point. `runeEncodable` treats a control rune as encodable (it becomes a space, not a degraded `?`); the dead ascii TAB special case is gone. `ascii` now prints C0/DEL as a space instead of `?` (intended, per BA).
- Plain-text `system`/`lp` renderers (`RenderText`, `RenderKitchenTicketText`, `RenderVoucherSlipText`) sanitise their input on entry, since they never go through `encodeText`.
- `RenderKitchenTicket`: `paperText` before `strings.ToUpper` for OrderType/Mode (ToUpper would turn an invalid byte into a valid U+FFFD).
- Kept: LRM/RLM/ALM, isolates U+2066–2069, ZWNJ/ZWJ (the ar/fa labels carry them).
- Tests: 3 `paperText` unit tests (incl. the real ar/fa labels and a 0-alloc check), 6 in `escpos_test.go`, 2 kitchen, 2 voucher slip, and `internal/pages/eod_print_control_5847_test.go` through `buildEODDoc`. Oracle: dirty render byte-equal to a sanitised twin built with a `strings.Replacer` (not `paperText`, so not circular), plus ESC p and newline counts.

## Review findings
- **R1 (low, test strength) — fixed.** The ESC p assertions counted the full 5-byte kick `ESC p 0 25 0xFA`; on cp858/win125x the trailing 0xFA already folded to `?` before the fix, so that assertion could not fail on those charsets (Tester's nit). All four sites (`escpos_test.go` ×2, `kitchen_test.go`, the EOD pages test) now count the `ESC p` prefix (`escP`). Re-verified: with the product files reverted, the count now goes red on every charset including cp858/win1250/1253/1254/1257 (e.g. `"cp858" kick=false: ESC p x20, want 0`).
- **R2 (low, doc accuracy) — fixed.** The ut-docs paragraph said the plain-text `system`/`lp` path also strips "at the encoder"; it strips at renderer entry, on a copy. Reworded, and noted that a literal line break in a single-line setting (footer via API/import) now prints as a space.
- **R3 (info) — accepted.** In the ESC/POS path `clip`/`wrapRunes`/`kvRow` measure the dirty text and `paperText` runs afterwards in `encodeText`. Safe: every replaced rune counts one column in `displayWidth` (Cc and U+2028–202E are not `zeroWidth`; an invalid byte decodes to one RuneError per byte both in `range` and in `paperText`), so widths are identical and layout holds. `wrapWords` (Line.Sub) splits on `unicode.IsSpace`, so a `\n`/U+2028 in a Sub row becomes a word break before sanitising: cosmetic only.
- **R4 (info) — accepted.** `TestPaperText_KeepsBidiMarksIsolatesAndJoiners` reads `web/locales/ar.json`/`fa.json` and requires at least one label with a bidi mark. A Weblate PR that removed every such mark from a locale would turn it red; unlikely, and the synthetic `keep` string remains the primary guard.

### Bypass hunt (no finding)
- Every printer write in `internal/pages` goes through `internal/print` renderers (`print_api.go` labels → `RenderLabelWithUnitPrice`, `kitchen_print.go` / `self_order_shop.go` → `RenderKitchenTicket`, receipts/EOD → `Render`/`PrintDocWithSlips`, designer preview → `RenderText`). No raw ESC/POS outside `internal/print` (only `escpos.go`, `raster.go` contain command bytes).
- Inside `internal/print`, every `b.Write`/`WriteString` of free text in the ESC/POS renderers goes through `enc`/`encodeText`; the rest are constant commands, pre-encoded rasters (Logo, TSEQR, TrackingQR) or CODE128 data, which `barcodeEncodable` restricts to printable ASCII 0x20–0x7E (otherwise the code prints as text via `enc`).
- `paperDoc` covers every string field of `Doc`, `Line`, `KV`, `VoucherBlock`; `paperTicket` every field of `KitchenTicket`/`KitchenItem`; `paperSlip` every field of `VoucherSlip` (checked against the struct definitions).
- `foldToCharmap`'s NFKD fold and the ascii NFD/NFC chain cannot produce a control rune from sanitised input.
- Hot path: `paperText` is a single decode pass with no allocation when clean; the copy helpers allocate only on the plain-text path.

## Verification
- Reviewer re-verified TDD in a throwaway `git worktree` (product files `escpos.go`, `kitchen.go`, `voucher_slip.go` reset to `origin/main`, `papertext.go` kept so the tests compile): 10 print tests and the EOD pages test fail for the right reason (`"utf8": control byte 0x1b in "A\x1bp\x00\x19\xfaB..."`, `got "A\x1bp\x00\x19\xfaB" want "A p  ?B"`, EOD `drawer-kick bytes reached the printer x7`, `37 newlines, clean report has 30`); worktree removed with `git worktree remove`.
- Tester: mutation runs A (product files reverted), B (`paperText` identity), C (over-stripping LRM/isolates) all caught; real driven run with the migrated DB and a sale line `"Apple\x1bp\x00\x19\xfa\nTOTAL 0.00"` through `printReceiptIn`, `printKitchen`, `buildEODDoc`+`Render` and system/lp mode (fake `lp`), on utf8/cp858/ascii: exactly one ESC p (the legitimate cash-sale kick, offset 2) on the receipt, none elsewhere, and the forged total stays on the item's own row.
- Gates: gofmt clean, `go build ./...`, `go vet` print/pages, `golangci-lint` 0 issues, `go test ./internal/print/... ./internal/pages/...` pass, `guard-fixed-dates.sh` and `guard-i18n.sh` pass. Tester ran full `go test ./...` (pass; internal/plugins needs the CI 20m timeout) and every build-job guard (pass; shellcheck not installed in the sandbox).

## Verdict
Approve (two low findings fixed in review).

## Deferred
- Drop `xPaperText` and add the X-report case to the AC2 test table once universal-till#2442 is on `main` (that helper lives only in that open PR).
- Reject control characters at name save time (local catalogue/settings saves), so they never reach the DB; this change only protects paper.

## CI follow-up

`desktop-shell`'s deadcode-baseline guard flagged `paperTicket` as newly unreachable. Its only caller,
`RenderKitchenTicketText`, is already on `scripts/ci/deadcode-baseline.txt` (no page previews a kitchen
ticket yet). The sanitising is now inlined in that renderer rather than growing the baseline. Behaviour
is unchanged, and `go test ./internal/print/` passes.
