# Help manual: receipt-charset auto-selection, corrected in en and translated into ar/fa/tr/de

**Card:** ut-docs#1891 (bullet 3) — the remainder of the ut-docs#1775 Turkish
Windows-1254 work. Bullets 1-2 (the `settings.printer.charset_win1254` pack
keys) shipped 2026-09-09 and are recorded in
`2026-09-09-printer-charset-win1254-key-1775.md`.

**Branch:** `fix/1891-help-charset-autoselect-translations`
**Complexity:** `easy` → independent review at Sonnet in a fresh-context
subagent, per the `reviewer` skill's model routing.

## What shipped

Item 8 of the printer-charset FAQ in `web/help/{ar,fa,tr,de}/printing.md` had
gone stale. All four still told merchants:

> Arabic, Farsi, Turkish and Greek tills stay on UTF-8 and are never switched
> automatically.

False since ut-docs#1733 (which added Windows-1250/1257/1253 auto-selection)
and doubly false for Turkish since ut-docs#1775 shipped Windows-1254. The
Turkish page was the sharpest case: it told a Turkish shop owner their till
stays on UTF-8, on the very page describing the code page that now
auto-selects Windows-1254 for them. `web/help/en/printing.md` had already
been corrected in universal-till#970; the four translations had not.

Each file gains a translation of the new English sentence covering
Windows-1250/1257/1253/1254 auto-selection, and its stale final sentence is
replaced by a translation of the corrected one — Turkish-**lira**-billing
shops stay on UTF-8 (a currency condition, because no code page here encodes
`₺`), and Arabic/Farsi tills stay on UTF-8 (a language condition).

**`web/help/en/printing.md` is also corrected** — see finding 1.

Translations were produced by the self-hosted `qwen3.6:35b-a3b` on the NAS
(`192.168.1.231:11434`), per `ut-docs/reference/translation.md`. This is why
the card carried `blocked:env`: that endpoint is unreachable from a cold
cloud cycle, so only a local NAS-reachable session could do it.

## Findings

### 1. `en/printing.md` claimed "all four cover both `€` and `£`" — factually wrong (FIXED)

Raised by the independent review, verified here two ways before acting.

The English sentence contradicted itself: its own parenthetical says
"Windows-1250 has no `£`, so a Croatian/Slovenian/Slovak shop in pounds stays
on UTF-8", and then the same sentence concluded "— all four cover both `€`
and `£`" over a list of Windows-1250/1257/1253/1254.

Verified against the codec tables (`cp1250` cannot encode `£`; `cp858`,
`cp1253`, `cp1254`, `cp1257` all can) and, decisively, against the product's
own ground truth in `internal/print/charset.go:59-64`:

> Windows-1250 has NO '£' (verified against charmap.Windows1250.EncodeRune …)
> so unlike cp858/win1257/win1253, win1250 only ever activates for EUR, never
> GBP (independent review finding, ut-docs#1733).

Corrected to "— Baltic, Greek and Turkish cover both `€` and `£`." A
Croatian/Slovenian/Slovak merchant billing in pounds could otherwise have read
the closing clause as promising that Windows-1250 fixes their `£` garbling,
when the till deliberately never switches them.

**Why fixed here rather than deferred.** The review correctly noted the error
is inherited, not introduced — `en/printing.md` was untouched by the original
diff. But this branch translates that exact clause into four languages, so
deferring meant knowingly shipping the contradiction into four more locales
and paying for a second five-locale translation round trip to undo it. The
fix is one clause in en plus the matching clause in each of the four
translations, all inside the sentence already being edited.

### 2. Round-one machine-translation defects (FIXED before review)

The first pass passed a strict structural check and was still wrong in four
places — the failure mode ut-docs#1891's own handoff warned about. Caught by
reading the output against the source, then fed back to the model as specific
complaints and regenerated (the retry loop in `reference/translation.md`
gotcha 3), rather than hand-patched:

- **fa** listed the languages as "کرواتی، اسلواکی یا اسلواک" — Slovak twice,
  Slovenian dropped.
- **tr** used the *country* names "Estonya, Letonya veya Litvanya" where the
  source names languages, and coined "kasiyesi" (from *kasiyer*, cashier).
- **ar** used "وسط أوروبا" and "البلطيق" where the shipped UI option labels
  are "أوروبا الوسطى" and "دول البلطيق", and typo'd "بالليليرا التركية" for
  "بالليرة التركية".
- **de** produced bare adjectives with no noun ("estnische, lettische oder
  litauische sendet"), and used "Durcheinander"/"gewechselt" where the
  paragraph already establishes "verstümmelt"/"umgestellt".

### 3. `make docs-shots` is NOT required for this change (claim verified, not a blocker)

The card's handoff asserted that editing `ar`/`fa`/`tr` `printing.md` changes
a topic hash and so needs a `make docs-shots` regen in the same PR. It does
not: `printing` declares no `routes:` in its front matter, so it is not one of
the 28 screenshotted topics and does not appear in
`web/help/img/manifest.json`. Confirmed independently by the review against
`e2e/tests-docs/lib.js`'s topic selection, and empirically —
`guard-docs-shots.sh` passes with all five files edited. Worth carrying
forward: the regen is ~14 minutes and has caused repeated mid-CI re-merges.

## Verified beyond the automated gates

- Each of the four translated paragraphs read against the English source for
  meaning, not just structure: language names are languages and the right
  ones; the pounds carve-out survives; the final sentence ties Turkish to
  **currency** and Arabic/Farsi to **language**; no false friends.
- The bolded code-page names checked character-for-character against the
  option labels the merchant actually sees — `web/locales/{ar,fa,tr}.json` and
  `ut-plugin-language-de/locales/de.json`, keys
  `settings.printer.charset_win125{0,3,4,7}`. All four locales match exactly.
- Structural parity with the English line: identical counts of `**` (16),
  backticks (12), and every literal token (CP858, Windows-1250/1253/1254/1257,
  UTF-8, v0.12.12, `€`, `£`, `₺`) across all five files.
- The stale claim is gone: none of the four still asserts that Turkish or
  Greek tills stay on UTF-8.

## Gates

`go build ./...`, `go vet ./...`, `go test ./internal/manual/...
./internal/pages/...`, `guard-help-topics.sh`, `guard-docs-shots.sh`,
`guard-i18n.sh`, `guard-compliance-claims.sh` — all green, re-run in full
after the finding-1 fix, not only before it.

## Verdict

**Safe to merge.** No new i18n keys, so no language-pack follow-up is implied
by this change and `lang-pack-drift` is unaffected.

## Deferred

Nothing from this review. ut-docs#1776 (verify the Windows-1250/1257/1253
charsets on real thermal hardware) and ut-docs#1926 remain open on their own
cards and are unrelated to the manual text.
