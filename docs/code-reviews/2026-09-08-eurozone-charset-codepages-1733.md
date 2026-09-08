# Code review — eurozone receipt-printer charset extension (ut-docs#1733)

**Date:** 2026-09-08
**Branch:** `fix/1733-eurozone-charset-codepages`
**Card:** ut-docs#1733 — Eurozone tills outside CP858's repertoire still print a mojibake currency symbol (ut-docs#1728 remainder)
**Reviewer:** independent Opus subagent (no sight of the implementer's reasoning; `complexity:medium`, per the model-routing table), findings triaged and fixed by the implementer
**Verdict:** SAFE TO MERGE after two blocker-class fixes and four non-blocker fixes below. All findings confirmed independently before being accepted as real; none were dismissed.

## What shipped

`internal/print.DefaultCharset` already resolved Western European eurozone/UK
stores onto CP858 (ut-docs#1728/#1243). This extends it to the eight
eurozone locales CP858 cannot represent without losing their own alphabet:
Greek, Croatian, Slovenian, Slovak, Estonian, Latvian, Lithuanian (Maltese
remains unsolved — see Deferred below), via three new ESC/POS code pages the
same printers already support (Epson's own published `ESC t` table):

| page | `ESC t n` | locales |
|---|---|---|
| Windows-1250 (Central European) | 45 | hr, sl, sk |
| Windows-1257 (Baltic Rim) | 51 | et, lv, lt |
| Windows-1253 (Greek) | 47 | el |

Key research finding written into the card before implementation: CP852 (the
"obvious" DOS-era Central European page) has **no Euro sign at all** —
verified against `charmap.CodePage852.EncodeRune('€')`. The modern Windows
125x variants got a later Euro update the DOS pages never did, and are
already in `golang.org/x/text/encoding/charmap`, resolving the original
card's "CP869/CP857 not in charmap" concern by using the modern equivalents
instead (Windows-1253/1254), not the DOS ones.

Also touched: `internal/pages/print_api.go`'s charset allow-list,
`internal/pages/kitchen_print.go`'s printer-safe-fallback (generalized from
a coarse "any non-ASCII under a restricted charset needs English" rule to an
actual per-rune encodability check, `print.Encodable`), the settings UI
dropdown, and i18n keys in all four core locales (en/ar/fa/tr) plus the
`ut-plugin-language-de`/`-es` packs (separate PRs, see below).

## Independent review — findings and disposition

### Blocker 1 (fixed): Windows-1250 has no `£`
`DefaultCharset`'s currency gate admits GBP as well as EUR, then routed
hr/sl/sk to win1250 regardless of currency. Windows-1250 has no U+00A3 — a
GBP + Croatian/Slovenian/Slovak till would trade a working `£` for a folded
`?`, worse than the utf8 pass-through it started from. **Fix:** win1250 now
only activates for EUR; GBP + hr/sl/sk stays on utf8 (win1257/win1253 both
carry `£` and are unaffected). Regression tests added:
`TestDefaultCharset`'s new GBP cases, `TestPoundSign_MissingFromWin1250OnlyAmongTheNewPages`.

### Blocker 2 (fixed): the fix was a no-op for every already-set-up till in the new markets
`AdoptDefaultPrinterCharset`'s one-shot marker (`printer.charset.adopted`)
was already burned to `"v1"` on `main` for every till that completed setup
under #1728 — including every Greek/Croatian/.../Lithuanian till, which
`DefaultCharset` still resolved to `"utf8"` at that time. The marker check
(`v != ""`) meant those tills would never be re-evaluated, so this diff
would have shipped with zero effect on any real till already in the field
in exactly the markets it targets. **Fix:** bumped `currentAdoptionVersion`
to `"v2"` and changed the check to only skip when the stored marker equals
the *current* version — exactly the mechanism the original code's own
comment described ("a future adoption pass can bump 'v1' without leaving a
second dead key behind") but that no prior change had actually exercised.
Regression test: `TestAdoptDefaultPrinterCharset_V1ToV2ReopensNewlyCoveredLocale`
(a till stuck on `v1` + `el-GR` + `utf8` gets moved to `win1253` and the
marker bumped; a subsequent deliberate re-pick of utf8 at the new version is
still respected, same as the existing single-version test already proved).

### Non-blocker 3 (fixed): `Encodable` false-positived on a literal `?` already in the source string
The first implementation scanned `encodeText`'s *output* for `?`, which
can't distinguish a genuine fallback from an ordinary `?` character already
present in the input. **Fix:** `Encodable` now checks rune-by-rune against
the real encode/fold decision (mirrors `encodeCharmap`'s own logic) instead
of scanning output bytes. Test: `TestEncodable_LiteralQuestionMarkInSourceIsNotADegradedRune`.

### Non-blocker 4 (accepted, not fixed): the `Encodable` generalization changes already-shipped Turkish kitchen-ticket behavior
Under the old coarse rule, a `tr` locale + `cp858`/`ascii` till fell back to
English kitchen tickets unconditionally (any non-ASCII text). Under the new
per-rune check, Turkish text without `ı` survives as an ASCII-folded string
("SİPARİŞ" → "SIPARIS") instead of falling back to English; text containing
`ı` (which has no decomposition and folds to `?`) still falls back per-key,
so a single ticket could in principle mix folded-Turkish and English lines.
**Decision:** keep the new behavior — it is a strict improvement (never
worse than the old blanket fallback, sometimes more readable to kitchen
staff than plain English) and no test locked the old exact behavior.
Flagging explicitly here per the review's own request for a stated decision
rather than a silent side effect, since it wasn't this card's stated goal.

### Non-blocker 5 (fixed): the operator-facing allow-list had no test coverage
`internal/pages/print_api.go`'s `POST /api/settings/printer` allow-list
*was* correctly extended, but nothing pinned it — reverting that one line
during review left the entire test suite green. Test added:
`TestPostSettingsPrinter_AcceptsNewEurozoneCharsets` (table-driven over all
three new values, asserts the 204 + persisted round-trip).

### Non-blocker 6 (deferred, documented): no end-to-end test proves a Greek shop keeps Greek kitchen tickets
The stated motivation for generalizing `kitchenTicketText` is that win1253
legitimately renders Greek, unlike ascii/cp858. This is unit-tested
directly (`TestEncodable`, `TestNewEurozoneLanguages_EverydayTextSurvivesWithoutDegrading`),
and the negative case end-to-end
(`TestBuildKitchenTicket_NewCharsetsFallBackToEnglishForNonLatinLocale`,
Arabic locale + win125x → English) — but the positive end-to-end case is
untestable in this repo today: `web/locales/` only ships en/ar/fa/tr; no
`el`/`hr`/`sl`/`sk`/`et`/`lv`/`lt` pack exists yet, so `httpx.T("el", ...)`
falls through to English regardless of charset. Will close naturally when
one of those packs lands (tracked as a new Backlog card, see below) —
noting the gap here rather than claiming coverage that doesn't exist.

### Non-blocker 7 (noted, not actionable): a new charset needs 7 coordinated edits
`encodeText`'s switch, `codepageSelectCmd`, the POST allow-list,
`kitchenTicketText`'s `restricted` list, `settings.html`, and 4 locale
files. `restricted` is genuinely load-bearing (not redundant) — it's what
stops finding 3/4's `Encodable` generalization from firing under plain
`utf8`. Structural observation, not a defect; no action taken.

### Nit 8 (fixed): stale comment
`print_api.go`'s `keyPrinterCharset` comment still listed only the old
three values; updated to match the other three call sites that already
mention win1250/1257/1253.

### Nit 9 (fixed): dropdown option order
Reordered to numeric order (1250, 1253, 1257) rather than an unexplained
1250/1257/1253.

## What was verified beyond automated tests

- `go build ./...`, `go vet ./...` clean.
- `gofmt -l` clean on every changed file.
- `bash scripts/ci/guard-i18n.sh`: all four core locales match key-for-key,
  1493 template keys resolve.
- Full `go test` across every `internal/...` package except
  `internal/plugins{,/oauth,/marketplace}` (excluded the same way CI's own
  main Test step excludes them, for the same reason — see that step's own
  comment): all green.
- Every `charmap` claim in the code comments (CP852 has no €; Windows-1250/
  1257/1253 do; each page's claimed alphabet coverage; Maltese covered by
  none of them; the `£` gap on Windows-1250 specifically) verified
  independently via throwaway `EncodeRune` probes, not taken on comment
  text alone.
- `ESC t` page numbers (45/47/51) cross-checked against a second, independent
  source ([bamarni/escpos](https://github.com/bamarni/escpos/blob/master/charset.go),
  which transcribes Epson's own table) after the primary source
  (`download4.epson.biz`) was unreachable from this sandbox (egress
  blocked) — internally coherent with the already-shipped `ESC t 19` (PC858)
  and the full 45–53 range.
- TDD-claim re-verification (reviewer, in an isolated worktree): reverted
  `kitchenTicketText`'s `restricted` list back to ascii/cp858-only, confirmed
  `TestBuildKitchenTicket_NewCharsetsFallBackToEnglishForNonLatinLocale`
  fails with real Arabic-text-leaking-through errors (not a compile error),
  restored, confirmed green again.

## Residual risk (explicitly not fully closed)

- **No real-hardware print run.** Everything here is byte-stream
  verification (same limitation this cloud session already flagged on
  ut-docs#1757's sibling cards) — AC #5 of the parent card requires a
  physical thermal-printer verification of at least one new page, which
  needs a local/interactive session with printer reachability. `blocked:env`,
  tracked as a new Backlog card (see below), same pattern as #1728/#1243/#1720.
- **Per-printer-model ESC/POS support for pages 45/47/51 is unconfirmed.**
  The command is standard per Epson's own reference, but an unsupported `n`
  on a non-Epson clone is typically ignored rather than erroring, which
  would leave the printer on its previous page rather than failing loudly.
  This is exactly what the physical verification above needs to catch.

## Deferred — new Backlog cards to open

1. Maltese (`mt`) currency-symbol charset — no code page found yet with both
   `€` and `ċ/ġ/ħ` (CP852, Windows-1250 both fail on 3 of 4 required runes).
2. Turkish (`tr`) printer-charset note for the `market:tr` track —
   Windows-1254 (`ESC t 48`) verified to have `€`/`ş`/`ı`/`ğ`, for whoever
   next touches TR receipt printing; deliberately excluded from this card.
3. Physical-hardware verification of win1250/1257/1253 on a real thermal
   printer — `blocked:env` for any cloud/cron session.
4. End-to-end kitchen-ticket coverage for a real non-Latin locale pack (el/
   hr/sl/sk/et/lv/lt) once one ships, closing non-blocker 6 above properly.

## Dependency: language packs

This diff adds 3 new keys to core's `web/locales/en.json`.
`lang-pack-drift` checks `ut-plugin-language-{de,es}` against core's *live*
`main` (advisory on this PR per its own `paths:`-scoped pull_request trigger,
blocking on push to `main`). Both packs' own PRs are prepared and pass their
own `check-key-drift.sh` against this branch's `en.json`
(`universaltill/ut-plugin-language-de#184`, `universaltill/ut-plugin-language-es#183`)
but cannot go green until core's keys actually land on `main` (their own CI
fetches core's live `main`, not this branch) — merged immediately after this
PR, per the same accepted-brief-red-window pattern already used for
ut-docs#1758 (Turkey PR #750's locale keys).
