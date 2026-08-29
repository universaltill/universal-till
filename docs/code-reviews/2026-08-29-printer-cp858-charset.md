# Opt-in cp858 printer charset for correct €/£ printing (ut-docs#1243)

## What shipped

Printed receipts rendered currency symbols as mojibake (`â‚¬2.50` instead of
`€2.50`) on single-byte-codepage ESC/POS thermal printers, because
`internal/print/escpos.go`'s `encodeText()` passed UTF-8 straight through for
every mode except `"ascii"`.

Fix: a **third, opt-in** charset mode, `"cp858"`, alongside the existing
`"utf8"` (default) and `"ascii"`. Nothing about the existing two modes
changes — Farsi/Arabic-market shops depend on real UTF-8 passthrough, and
`TestUTF8PassThrough`'s Farsi fixture stays byte-for-byte identical.

- `internal/print/escpos.go` — `encodeText()` restructured from an early-return
  `if` into a `switch` with `ascii` / `cp858` / `default` arms. The `cp858` arm
  encodes **per rune** via `charmap.CodePage858.EncodeRune(r)`, falling back to
  a literal `?` for anything CP858 lacks. Deliberately *not*
  `CodePage858.NewEncoder()` / `transform.String`: the stock encoder's
  substitution byte is `0x1A`, not `?`, so the per-rune loop is what makes the
  fallback match the `ascii` arm exactly.
- New `codepageSelectCmd(charset)` helper returning ESC/POS `{0x1b, 0x74, 0x13}`
  (ESC t 19 → PC858, the Euro variant of PC850) for `"cp858"` and `nil`
  otherwise. Called immediately after `cmdInit` in `Render()`, `RenderLabel()`
  (`escpos.go`) and `RenderKitchenTicket()` (`kitchen.go`).
- `internal/pages/print_api.go` — the settings POST handler's charset
  validation went from `if charset != "ascii" { charset = "utf8" }` to an
  explicit `switch charset { case "ascii", "cp858": default: charset = "utf8" }`
  allow-list.
- `internal/pages/kitchen_print.go` — **second, separate bug**, found by this
  session's Tester pass and not in the original Dev brief. The ut-docs#261
  safety net that degrades a non-Latin (ar/fa) kitchen-ticket translation to
  English when the configured charset can't render it was hardcoded to
  `charset == "ascii"`. cp858 is equally a Latin-only single-byte code page, so
  a cp858-configured till fell straight through the guard and would have
  printed untranslated Arabic/Farsi as a run of `?`. Condition widened to treat
  both `"ascii"` and `"cp858"` as restricted.
- `web/ui/pages/settings.html` — one new `<option value="cp858">` in the
  existing printer charset `<select>`.
- `web/locales/{en,ar,fa,tr}.json` — new key `settings.printer.charset_cp858`.
- `web/help/{en,ar,fa,tr}/printing.md` — one new numbered troubleshooting step
  per locale.
- `internal/print/transport.go` — doc comment only.

## Tests

- `internal/print/escpos_test.go`, 5 new tests: `€`/`£` byte encoding
  (`0xD5` / `0x9C`); unmappable-rune `?` fallback on Arabic; and code-page-select
  emission in `Render` / `RenderKitchenTicket` / `RenderLabel`. Each of the
  three emission tests asserts **both** that cp858 emits `ESC t 19` immediately
  after init *and* that `""` / `"utf8"` / `"ascii"` do not emit it anywhere in
  the stream — so the "existing modes unchanged" promise is pinned, not assumed.
- `internal/pages/kitchen_print_test.go`, 1 new test mirroring the existing
  `TestBuildKitchenTicket_AsciiCharsetFallsBackToEnglishForNonLatinLocale`
  precedent for cp858.

## Independent verification (re-run personally, not taken on faith)

Reviewed in an isolated worktree fast-forwarded onto the WIP commit, diffed
against its parent `01cbdf0`.

**Test-first re-verified, both fixes, by reverting production code only and
keeping the new tests.**

`git checkout HEAD~1 -- internal/print/escpos.go internal/print/kitchen.go
internal/print/transport.go internal/pages/print_api.go`, then
`go test ./internal/print/ -run CP858 -v`:

```
=== RUN   TestCP858EncodesCurrencySymbols
    escpos_test.go:282: euro: encodeText = e282ac322e3530, want d5322e3530
    escpos_test.go:285: pound: encodeText = c2a3312e3030, want 9c312e3030
--- FAIL: TestCP858EncodesCurrencySymbols (0.00s)
=== RUN   TestCP858UnmappableRuneFallsBackToQuestionMark
    escpos_test.go:295: encodeText = d8acd985d8b9, want 3f3f3f (one '?' per unmappable rune)
--- FAIL: TestCP858UnmappableRuneFallsBackToQuestionMark (0.00s)
=== RUN   TestRenderCP858EmitsCodepageSelect
    escpos_test.go:308: cp858 render must emit ESC t 19 immediately after init
--- FAIL: TestRenderCP858EmitsCodepageSelect (0.00s)
=== RUN   TestRenderKitchenTicketCP858EmitsCodepageSelect
    escpos_test.go:324: cp858 kitchen ticket must emit ESC t 19 immediately after init
--- FAIL: TestRenderKitchenTicketCP858EmitsCodepageSelect (0.00s)
=== RUN   TestRenderLabelCP858EmitsCodepageSelect
    escpos_test.go:339: cp858 label must emit ESC t 19 immediately after init
    escpos_test.go:342: cp858 label price must carry the CP858 euro byte 0xD5
--- FAIL: TestRenderLabelCP858EmitsCodepageSelect (0.00s)
FAIL
```

Genuinely red, and red *for the claimed reason*: `e282ac` is the raw UTF-8
encoding of `€`, `c2a3` of `£`, `d8acd985d8b9` of `جمع` — i.e. exactly the
pass-through mojibake the card exists to fix, not an assertion typo. Restoring
the four files returns all five to green alongside `TestUTF8PassThrough` and
`TestAsciiCharset`.

Same cycle for the kitchen_print.go fix —
`git checkout HEAD~1 -- internal/pages/kitchen_print.go`:

```
=== RUN   TestBuildKitchenTicket_AsciiCharsetFallsBackToEnglishForNonLatinLocale
--- PASS: TestBuildKitchenTicket_AsciiCharsetFallsBackToEnglishForNonLatinLocale (0.16s)
=== RUN   TestBuildKitchenTicket_CP858CharsetFallsBackToEnglishForNonLatinLocale
    kitchen_print_test.go:343: ticket.Station = "المطبخ", want English fallback "KITCHEN" under cp858 charset
    kitchen_print_test.go:346: ticket.OrderLabel = "الطلب", want English fallback "ORDER" under cp858 charset
    kitchen_print_test.go:349: ticket.OrderType = "تيك أواي", want English fallback "Takeaway" under cp858 charset
--- FAIL: TestBuildKitchenTicket_CP858CharsetFallsBackToEnglishForNonLatinLocale (0.16s)
```

Worth recording that the pre-existing **ascii** test still passes against the
*old* file in the same run — that is the proof the widening is purely
additive and preserves `charset=="ascii"` behaviour exactly, rather than
rewriting it.

**Full gate, after all review fixes below:**

- `gofmt -l internal/ web/` — no output.
- `go build ./...` — clean. `go vet ./...` — clean.
- `go test ./...` — **every package green**, full suite, no exclusions and no
  pre-existing failures to excuse (`internal/print` 0.029s, `internal/pages`
  94.211s, `internal/data` 69.983s, …).
- All 29 CI-blocking guards from `.github/workflows/ci.yml`'s `build` job run
  individually — all `ok`, including `guard-i18n` ("1304 template keys resolve;
  all locales match en.json"), `guard-help-topics`, `guard-compliance-claims`,
  `guard-data-access`, `guard-docs-shots`.

**Coverage checks I re-derived rather than trusting:**

- Every `Charset` assignment under `internal/` (`receipt_designer.go:210`,
  `eod_api.go:169`, `invoice_page.go:199`, `kitchen_print.go:128`,
  `print_api.go:150,452`, `RenderLabel` at `print_api.go:504`) resolves from
  `printerConfig(...).Charset`, so all of them pick cp858 up automatically. No
  caller needed separate treatment.
- Grepped every `"ascii"` occurrence repo-wide for another latent
  ascii-only special case of the `kitchen_print.go` kind. The only remaining
  hits are the escpos switch arm itself, tests, the settings template option,
  and prose in an old review record. None is a second gap.
- No raw SQL introduced outside `internal/data` / `internal/db` (the new test's
  `INSERT` statements are in a `_test.go`, matching the existing tests beside
  them; `guard-data-access` confirms).
- Neither of the two recurring pipeline bug classes applies: the diff writes no
  files at all (only `t.TempDir()` appears), so there is no missing
  `os.MkdirAll` and no cwd-relative path that should have been `paths.Data(...)`.
- No real client or shop name in test data. `Coca-Cola Can 330ml` /
  `5000000000011` is the long-standing demo-seed fixture with explicit prior
  review precedent (2026-08-07, 2026-08-08 records); it is a consumer product
  brand, not a customer. Nothing secret-shaped introduced.

## Findings

1. **Blocking, found and fixed at review — `guard-docs-shots` was red.** The
   diff changed `web/ui/pages/settings.html` and non-test `internal/pages/*.go`,
   which are both in the guard's hashed app surface, but `web/help/img/manifest.json`
   was never regenerated. CI would have failed on push. Confirmed the guard was
   **green at the parent commit** and red only because of this diff (reverted the
   three surface files → `✓ docs-shots guard ... (surface 307631752f3a…)`;
   restored → fail). Fixed by running `make docs-shots` (92 screenshots, all
   passed) and committing the regenerated manifest.

   Two incidental notes, both deliberate. First, `settings.png` did **not**
   change — correct, because a non-selected `<option>` is invisible in a closed
   `<select>`, so the new option genuinely alters no pixels. Second, the run
   reused `/opt/pw-browsers` Chromium 141 against a pinned 149 (the sanctioned
   ut-docs#622 cloud-session fallback, which warns loudly and non-fatally); that
   produced two unrelated re-renders (`web/help/img/en/invoices.png`,
   `web/help/img/fa/sell.png`) on screens this diff does not touch. Those were
   **reverted** rather than committed — the guard hashes source files and topic
   markdown, not PNG bytes, so it stays green, and the originals rendered by the
   correctly-pinned browser are more trustworthy than noise from a mismatched
   one. Net result is a one-line manifest change (the surface hash) and no
   unrelated binary churn. The `printing` topic has no screenshot at all (not a
   routed topic, absent from the manifest), so no topic hash moved.

2. **Real, fixed at review — the manual actively mis-advised ar/fa/tr shops.**
   The new help step told every locale to select CP858 to fix `€`/`£`, and
   closed with "the till's language support on screen is unaffected" — reassuring,
   but silent on the fact that *printed* non-Latin text breaks under this option.
   I verified by probe that CP858 maps Arabic, Farsi **and Turkish** to `?`. The
   Turkish case is the sharp one and is not obvious: CP858 has no `ş`, and it has
   no `ı` either — `ı`'s CP850 slot at `0xD5` is precisely the byte CP858 gives
   up to gain `€`. So `"Sipariş"` prints `Sipari?` and `"kırmızı"` prints
   `k?rm?z?`. A Turkish shop following the tr manual to get `€` would silently
   garble its own item names. Added one sentence to all four locales stating that
   CP858 covers Western European letters only and that UTF-8 should stay selected
   if receipts carry Arabic/Farsi/Turkish; the tr copy names `ı`/`ş` explicitly.

3. **Real but non-blocking, accepted as-is, documented in code.** The cp858 arm
   passes C0/DEL control bytes (`\n`, `\r`, `ESC`, `NUL`, `0x7f`) through
   unchanged, where the ascii arm maps them to `?`. A stray `0x1B` in an item
   name would therefore reach a cp858 printer as a command byte. Not a
   regression and not this card's to fix: the **default** `utf8` mode already
   behaves identically today, so cp858 is no worse than what nearly every till
   runs — it simply isn't an improvement on it. Left as-is rather than making
   cp858 quietly stricter than the default, and recorded in the `encodeText`
   comment so the next reader sees the difference is deliberate.

4. **Real but non-blocking, deferred as a product question.** The cp858 arm does
   no NFD diacritic folding, so where ascii transliterates (`"Sipariş"` →
   `"Siparis"`, readable) cp858 emits `?`. A fold-then-encode fallback for
   unmappable runes would strictly improve this, but whether an option labelled
   "Western Europe" should transliterate at all is a product call, not a review
   fix. Worth a follow-up card. Recorded in the code comment alongside #3.

5. **Checked, not a problem.** `codepageSelectCmd` cannot double-fire or land in
   the wrong order. `cmdInit` is the first write in all three renderers and the
   select is the second, before any text and before `cmdKickDrawer` /
   `cmdKickDrawerPin5` (which are drawer pulses, not text state). The multi-copy
   label path (`print_api.go:504`) concatenates whole `RenderLabel` outputs, so
   the select repeats once per copy — that is *required*, not a bug, because each
   copy re-emits `ESC @`, which resets the printer's code page. Verified the
   empty string encodes to empty, and invalid UTF-8 degrades to one `?` per bad
   byte (Go's `range` yields `U+FFFD`, which CP858 cannot encode) rather than
   corrupting the stream.

6. **Checked, not a problem.** The `print_api.go` allow-list rewrite has no
   behaviour difference beyond admitting cp858: every previously-accepted value
   maps to the same result, and any unrecognised value still falls to `"utf8"`.

## Translations

Sanity-checked directly (written by the implementing agent, translation endpoint
unreachable). All four label strings are correct and, importantly, each reuses
its own locale's *existing* term for the surrounding "Characters" field —
`المحارف` / `نویسه‌ها` / `Karakterler` match the `settings.printer.charset` values
already in those files, so the new option reads as native rather than bolted on.
`أوروبا الغربية` / `اروپای غربی` / `Batı Avrupa` all correctly render "Western
Europe". The help paragraphs match each file's numbered-troubleshooting register,
use correct ZWNJ in Farsi, and correctly write the Settings→Printer arrow for
RTL. Nothing reads as machine-translated. One cosmetic, non-blocking note: in the
Farsi screenshot the parenthetical bidi-reorders to `(£/€ — CP858)`; that is
standard Unicode bidi for an LTR run inside RTL text, still legible and
identifying, and fixing it would need bidi isolates in the locale JSON — out of
scope.

## UX

Applied lightly, per the card — this is one `<option>` added to an existing
select, not a new surface. Read the two 1280x900 screenshots taken this session
(English/LTR and Farsi/RTL). The label "Western Europe (CP858 — €/£)" fits the
select with no truncation in both, and the Farsi shot confirms correct RTL
layout (labels right-aligned, select chevron on the leading edge). No need to
re-drive the UI.

## Disclosed verification gap

No real thermal printer hardware is available to this pipeline — the same
standing gap as ut-docs#1260 and the 2026-07-16 printer-type-aware review
record. Verification here is at the **byte level only**: unit tests asserting
the exact CP858 byte values (`0xD5`, `0x9C`) and the exact ESC/POS command
bytes (`1b 74 13`). That `ESC t 19` is the right selector for PC858, and that a
given printer's firmware honours it, is taken from the ESC/POS spec, not
observed on a device. This is explicitly in scope as accepted per the ticket,
and is called out here rather than left implicit.

## Safe to merge

**Yes**, with the review fixes above included. The core change is well-shaped:
strictly additive, the existing `utf8`/`ascii` byte streams are pinned by
negative assertions rather than merely left alone, both bugs are genuinely
test-first, and the second bug (`kitchen_print.go`) was a real latent gap the
original brief did not ask for. The one blocking issue was a missed CI guard,
not a defect in the fix itself. Full suite and all 29 guards green after the
fixes.
