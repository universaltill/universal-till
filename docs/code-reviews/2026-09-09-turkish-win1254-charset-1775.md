# Code review — Turkish receipt-printer charset (ut-docs#1775)

**Date:** 2026-09-09
**Branch:** `fix/1775-turkish-win1254-charset`
**Card:** ut-docs#1775 — Turkish (tr) receipt-printer charset: Windows-1254 verified to cover €/ş/ı/ğ
**Reviewer:** independent Opus subagent (no sight of the implementer's reasoning; `complexity:medium`, per the model-routing table), findings triaged and fixed by the implementer
**Verdict:** SAFE TO MERGE after one fixed comment-accuracy finding below. No correctness bugs found; every `EncodeRune`/`ESC t` claim was independently re-verified against `golang.org/x/text/encoding/charmap` and Epson's own published reference.

## What shipped

`internal/print.DefaultCharset` already resolves Western European and the
eight `ut-docs#1733` eurozone locales onto a real code page. This extends it
with a ninth page for Turkish, split out of #1733 on purpose (own
`market:tr` track — see #1733's own "Deferred" section, item 2):

| page | `ESC t n` | locale gate |
|---|---|---|
| Windows-1254 (Turkish) | 48 | `tr`, currency EUR or GBP |

Verified via `charmap.Windows1254.EncodeRune`: covers the full Turkish
alphabet (ş/ı/ğ/Ş/İ/Ğ) plus **both** `€` (0x80) and `£` (0xA3) — unlike
Windows-1250, which lacks `£` and is therefore gated EUR-only, win1254 gates
on either currency, same as win1257/1253. Windows-1254 does **not** encode
`₺` (U+20BA, verified `ok=false`) — TRY is therefore deliberately **not**
added to `DefaultCharset`'s currency gate; a TRY store keeps the existing
utf8 pass-through unchanged. Windows-1254 also turned out to natively
encode the Word/Excel typography (`œ`/`Œ`, en-dash, curly quotes, ellipsis,
bullet) that win1250/1257/1253 needed the shared fold table for — a genuine
difference from those three, not assumed from the pattern.

Also touched: the same seven call sites #1733 already established as the
checklist for a new charset (`encodeText`/`runeEncodable`/`codepageSelectCmd`,
the `POST /api/settings/printer` allow-list, `kitchenTicketText`'s
`restricted` set, the settings UI dropdown, all four core locale files),
plus `AdoptDefaultPrinterCharset`'s adoption-version bump (`v2` → `v3`) so a
till already stuck on utf8 under v2 gets re-evaluated, and a new
`docs-shots` regeneration (required by `guard-docs-shots.sh`, which
actually failed until run — not just advisory).

## Independent review — findings and disposition

### Non-blocker 1 (fixed): a code comment overclaimed the fold table was "essentially unused" for win1254
`escpos.go`'s `encodeText` comment claimed `foldToCharmap`'s tables go
"essentially unused" for win1254 since Word/Excel typography encodes
natively. Independent re-check against `EncodeRune` showed this is only
true for the *typography* subset — Central European/Baltic letters this
page lacks (`č`/`ā`/`ž`/`ą`/`ė`/`ū`/`Ž`, confirmed `ok=false`) and
`ĳ`/`Ĳ`/`ŀ`/`Ŀ`/`№` still reach the NFKD-decomposition and
punctuation-substitution fold steps exactly as they do for
win1250/1257/1253. **Risk if left as written:** a future maintainer reads
the overclaim and prunes or short-circuits the fold path for win1254,
silently reintroducing `?` for e.g. a Croatian item name on a Turkish-till.
**Fix:** narrowed the comment to the verified typography claim and named
the runes that still exercise the fold, re-verified against `EncodeRune`
independently before writing the new text (not taken on the reviewer's word
either).

### Non-blocker 2 (accepted, not a code change — stated here and in the PR): the automatic default does not reach a TR/TRY till
Universal Till's own country-settings seed (`internal/data/country_settings_repo.go`)
maps Turkey to currency `TRY`, and `httpx` gives TRY the `₺` display symbol
on every money line — so a Turkish shop that picked "Turkey" in the setup
wizard can never satisfy `DefaultCharset`'s EUR/GBP gate and stays on utf8,
same as before this card. The population this card's automatic default
actually reaches is a Turkish-*language* shop pricing in EUR/GBP (a
Turkish-run business in the eurozone/UK, or the `market:tr` EUR-pricing
case ut-docs#1828/#1883 already anticipate) — real, but narrower than "TR
market". Reviewed the win1250-EUR-only precedent for the same shape and
confirmed the reasoning is structurally identical: trading one broken
currency symbol for another single-byte page's `?` is never a fix, so
staying on utf8 for TRY is correct, not a gap to guess past. **Decision:**
keep as designed — win1254 is a real, verified improvement for the
currency it can actually represent, and inventing a `₺`-capable code page
that doesn't exist would be guessing. Stated explicitly in the PR body and
the card close-out so #1775 isn't read as "Turkish market fully covered."

### Non-blocker 3 (accepted, not a code change): the v2→v3 adoption bump's blast radius is every locale, not just Turkish
`AdoptDefaultPrinterCharset`'s one-shot marker re-opens the "utf8 → real
code page" question for **every** till still on utf8 when the version
bumps, not only Turkish ones — a German/Greek/Croatian/etc. operator who
deliberately re-selected utf8 after the v2 pass gets silently re-evaluated
(and, per the file's own existing doc comment, potentially moved again) at
v3. This is the exact same mechanism and the exact same accepted tradeoff
the v1→v2 bump already made for the #1733 locales — consistent with prior
practice, not a new defect — but nothing pinned the non-Turkish side effect
with a test, and nothing stated it as a deliberate cost of this specific
bump. **Decision:** keep the mechanism (it's what makes the fix reach a
till already in the field, same justification as v1→v2), state the blast
radius explicitly in the PR body rather than let it be an unstated side
effect.

### Non-blocker 4 (noted, no code change): the Turkish dropdown label doesn't warn about `₺`
`web/locales/tr.json`'s new `settings.printer.charset_win1254` row reads
"Türkçe (Windows-1254 — €/ş/ı)" — accurate and matches the exact format of
every other row in that dropdown, but on a Turkish-language till this is now
the obvious-looking pick, and an operator on TRY who selects it by hand
would fold every `₺` to `?` (strictly worse than the utf8 they're on).
Adding a caveat clause to the shipped Turkish string would be new
user-facing prose, which this ecosystem's `ai-self-hosted-only` ADR
requires to go through the self-hosted NAS translation pipeline
(`reference/translation.md`) — unreachable from this cloud session
(verified: `curl` to `192.168.1.231:11434` times out). Left the string
matching the existing pattern; flagged as a known trap in the card instead
of guessing a translation past the ADR.

### Nit 5 (fixed): three test names claimed "eurozone" for a diff that now includes Turkey
`TestRender_DefaultCharsetForNewEurozoneLocales_...`,
`TestNewEurozoneLanguages_...` (`internal/print/charset_test.go`) and
`TestPostSettingsPrinter_AcceptsNewEurozoneCharsets`
(`internal/pages/print_api_test.go`) all gained a Turkish/win1254 case —
correct coverage, inaccurate name (Turkey isn't in the eurozone). Renamed
to `...NewSingleBytePageLocales...`/`...NewSingleBytePageLanguages...`/
`...NewSingleBytePageCharsets`, updated the doc comments and cross-references
that named the old identifiers.

### Nit 6 (fixed): ragged comment reflow
`internal/pages/kitchen_print.go`'s `kitchenTicketText` doc comment left an
orphan short line after the win1254 clause was inserted. Rewrapped.

### Nit 7 (noted, not actionable — pre-existing, out of scope): `cp858`'s ESC t byte is hex, every other arm is decimal
`escpos.go`'s `codepageSelectCmd` writes `0x13` for cp858 while
45/47/48/51 are decimal, and its own doc comment above already talks in
decimal. Predates this card (#1243) and isn't touched by this diff; per
this pipeline's own scope discipline, left alone rather than widening this
change — a one-character cleanup for whoever next touches that function.

## What was verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` all clean on every changed
  file, before and after the review's fixes.
- Full `go test -count=1 ./internal/print/... ./internal/pages/...
  ./internal/settings/...`: green, both before the review's fixes and after
  (re-run to confirm the renames/comment edits didn't break anything).
- `bash scripts/ci/guard-i18n.sh`: all four core locales match key-for-key,
  1529 template keys resolve.
- `bash scripts/ci/guard-help-topics.sh`: clean.
- `bash scripts/ci/guard-data-access.sh`: clean (this diff touches no SQL).
- `bash scripts/ci/guard-docs-shots.sh`: **failed** on the first run
  (`internal/pages/print_api.go`/`kitchen_print.go` and
  `web/ui/pages/settings.html` are part of the screenshotted app surface) —
  regenerated via `make docs-shots` (reusing the sandbox's pre-installed
  Chromium per `resolve-chromium.sh`/ut-docs#622), all 104 screenshots
  passed, guard re-run clean afterward. Only 2 of the 104 PNGs actually
  changed bytes (`ar/multitill.png`, `en/sell.png`, both 1-2 bytes —
  compression/rendering noise, not a visible regression; the printer
  settings screenshot itself is pixel-identical, as expected — a closed
  `<select>` doesn't render its unselected options).
- Every `EncodeRune` claim in the diff's comments (€/£/₺ and the full
  Turkish alphabet, plus the review's own follow-up claims about `œ`/`Œ`/
  `ĳ`/`ŀ`/`№`/`č`/`ā`/`ž`/`ą`/`ė`/`ū`/`Ž`) independently re-verified via a
  throwaway `charmap.Windows1254.EncodeRune` probe, twice (once by the
  reviewer, once by the implementer confirming the reviewer's specific
  counter-examples before rewriting the comment).
- `ESC t 48` cross-checked by the reviewer against Epson's own published
  TM-series ESC/POS reference and code-page table (both cited in the
  review), internally consistent with the already-shipped 19/45/47/51.
- New end-to-end coverage:
  `TestSettingsPage_PrinterCardHasWin1254CharsetOption` — a real
  `httptest` GET `/settings` render, not just a template-file diff, proving
  the dropdown option and its translated label actually reach the page.
- Turkish kitchen-ticket printed strings (`Paket`/`Karışık`/`Burada`/
  `MUTFAK`/`SİPARİŞ`) checked against win1254's `Encodable` — all render
  natively, so `tr` becoming the first bundled UI locale that can default to
  a restricted charset doesn't silently drop any of these to English.

## Residual risk (explicitly not fully closed)

- **No real-hardware print run** — same `blocked:env` limitation as
  #1733/#1728/#1243/#1720; byte-stream verification only.
- **Per-printer-model ESC/POS support for page 48 is unconfirmed** on
  non-Epson clones, same caveat #1733's own review already recorded for
  pages 45/47/51.
- **Merge is intentionally NOT happening in this cycle.** This diff adds a
  key to `web/locales/en.json`; `lang-pack-drift` is advisory on the PR but
  **blocking on `main`**, and the `reviewer` skill's own standing rule
  requires the `ut-plugin-language-{de,es}` packs to land the same key
  first. Translating it requires this ecosystem's self-hosted NAS model
  (`ai-self-hosted-only` ADR) — unreachable from this cloud sandbox
  (verified: connection to `192.168.1.231:11434` times out). Left `blocked:env`
  on the card rather than guessing a translation past the ADR or merging
  ahead of the pack sync and leaving `main` red.

## Deferred — new Backlog cards to open

1. `ut-plugin-language-de`/`-es` pack sync for `settings.printer.charset_win1254`
   (and the `web/help/{ar,fa,tr,de}/printing.md` translation of this same
   PR's English-only manual update) — needs the self-hosted NAS translation
   pipeline, `blocked:env` for this cloud session.
2. A real Lira-capable code page for TRY tills, if one is ever found —
   Windows-1254 confirmed NOT to have `₺`; this diff does not close the
   "Turkish market fully covered" question, only the alphabet/EUR-GBP slice
   of it (see non-blocker 2 above).
