# Code review — receipt rows measure display width, not runes (ut-docs#4685)

**Date:** 2026-10-19 · **Lane:** lane:cloud-41 · **Author model:** Opus 5.5 · **Reviewer model:** Fable (independent)

## What shipped

`internal/print` laid receipt rows out by rune count. A zero-width
character counts as a rune but takes no column. So the fa label
`receipt.change` ("باقی‌مانده", with a ZWNJ) ended its row one column
short of the other rows, and combining marks (Arabic harakat, decomposed
accents) did the same.

- New `displayWidth`/`zeroWidth` (`internal/print/width.go`). U+200C ZWNJ,
  U+200D ZWJ and Unicode Mn/Me take 0 columns; every other rune takes 1.
- `kvRow`, the one-row/two-row threshold in `layoutLine`, `wrapWords`,
  `wrapRunes`, `clip`, and the centring in `RenderText` and
  `RenderKitchenTicketText` all measure with it. These are used by both the
  preview (`RenderText`) and the printed ESC/POS text (`Render`).
  `clip` keeps a zero-width rune with the character before it, so a
  combining mark is never cut off its base.
- Single-byte code pages (`ascii`, `cp858`, `win125x`): text is
  NFC-composed first, so `e`+U+0301 prints as the page's `é`. A leftover
  zero-width rune is dropped instead of printed as `?`. Without this, a row
  padded for 0 columns would print 43 columns on a 42-column head.
  `Encodable` now reports ZWNJ/ZWJ as encodable, since they are dropped,
  not degraded.
- en output is unchanged: ASCII and precomposed text measure the same as
  before. The pre-existing golden tests in `escpos_test.go` pass unchanged.

## Findings (Fable review) and outcome

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | ZWNJ/ZWJ (and the test fixtures' marks) were stored as raw invisible characters inside rune and string literals. An editor shows `''`, which invites a "fix" that deletes them. | **Fixed:** every one is now a `\u` escape. The one raw ZWNJ left in a comment was reworded. |
| 2 | minor | On `ascii`/`cp858`/`win125x` a ZWNJ printed as `?` (one column) while `kvRow` padded it as zero, so the row overflowed `Width` by one column. A decomposed accent printed as `e?`. | **Fixed:** NFC first, then drop zero-width runes in the ascii arm and `encodeCharmap`. Test: `TestCodePageRowsStayWidthWithZeroWidthRunes`, which covers all six code pages. |
| 3 | minor | `wrapRunes` (Meta lines, voucher code, tracking URL) still wrapped by rune count, and fa Meta labels carry ZWNJs. | **Fixed:** it wraps by columns, and each cut uses `clip`. Test: `TestWrapRunesMeasuresDisplayWidth`. |
| 4 | minor | Other zero-width format characters (LRM/RLM/ZWSP/SHY) and East-Asian double width are not measured. | **Accepted.** This is the card's scope. No `receipt.*` key carries them today. |
| 5 | nit | The AC test measured alignment only with `displayWidth` itself. | **Fixed:** it also asserts that the row ends in label + exact padding + amount. |
| 6 | note | `internal/pages/eod_api.go` `footerRow`/`eodRow`, the VAT-table sizing (plus a stale "clips at Width RUNES" comment) and `cloudsync_print_report.go` still pad by rune count. | **Deferred** to ut-docs#4689 (Backlog). |

## Verification

- TDD: the reviewer reverted `escpos.go`/`kitchen.go` to their state before
  the fix and saw 6 of the 7 new tests fail (41 vs 42 columns, two rows
  instead of one, 15 vs 16 centring spaces, `clip("aéb",2)="ae"`). I did
  the same for the fixes from the review: the three new tests fail against
  the pre-review encoder and `wrapRunes`, and pass with the fix.
- `gofmt`, `go build ./...`, `go vet ./...`, `golangci-lint` (0 issues),
  core-neutral and deadcode-baseline guards, and full `go test ./...`.
- Not checked visually: no screen changed. The receipt designer's preview
  is plain text. The change moves an amount by one column on rows with
  zero-width characters.

## Verdict

Safe to merge.
