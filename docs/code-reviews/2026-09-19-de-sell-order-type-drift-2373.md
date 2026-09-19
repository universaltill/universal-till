# Code review: web/help/de/sell.md order-type drift (#2282/#2309 never reached DE)

- **Card:** universaltill/ut-docs#2373 — independent review finding on
  ut-docs#2371 (universal-till#1226), out of that card's scope.
  `web/help/de/sell.md`'s "Vor Ort oder Außer Haus" section still
  described the per-line 🍽️/🥡 control and the **Gemischt** chip, both
  removed by ut-docs#2309 (universal-till#1204), and never received the
  "When you're asked" paragraph that ut-docs#2282 added to en/ar/fa/tr
  and ut-docs#2371 updated. `scripts/ci/guard-help-drift.sh` couldn't see
  it — it compares structural counts, and the German topic's counts
  still matched the (stale) English structure it was compared against
  historically; the content itself had gone false.
- **Repo:** `universal-till`
- **Reviewer:** independent fresh-context Sonnet subagent
  (`complexity:easy` — Sonnet builds, Sonnet fresh-context reviews, per
  `MODEL-ROUTING.md`).

## What shipped

- `web/help/de/sell.md`: rewrote the three paragraphs under "## Vor Ort
  oder Außer Haus" to match `web/help/en/sell.md`'s current "## Dine-in
  or takeaway" section on `main`:
  - Paragraph 1 (whole-sale segment behaviour) gained the missing closing
    sentence ("Vor Ort oder Außer Haus ist eine Wahl für den gesamten
    Verkauf — es gibt keine separate Steuerung pro Artikel.").
  - The obsolete per-line 🍽️/🥡 control paragraph and the "same product on
    two lines" paragraph (both describing behaviour removed by #2309) were
    replaced with a translated "**Wann Sie gefragt werden.**" paragraph
    covering all three prompt placements (default top-of-basket,
    before-first-item with New-Sale/receipt-exit triggers and
    re-ask-on-dismiss, at-Pay), matching EN's "When you're asked".
  - The historical-Mixed-basket note was kept and retranslated to match
    EN's current wording (a held sale from before the deferred prompts
    existed, or from an older till version, can still show Mixed; that's
    historical, not creatable from the sell screen any more; a mixed sale
    can still have a table, only all-takeaway can't).
- No other `web/help/de/*.md` topic carried the same drift (checked per
  requirement #4 — only `sell.md` mentioned 🍽️/🥡/Gemischt).
- No screenshot regeneration needed — text-only change, `sell.md` isn't
  in the docs-shots route manifest for this section.

## Review findings

**None — PASS, no nits.** The independent reviewer compared the DE text
paragraph-by-paragraph against EN's source-of-truth section, checked
terminology consistency against the rest of the same file (Warenkorb,
Vor Ort/Außer Haus, Verkauf, gehalten/Halten/Abrufen, the existing
"Einstellungen → Anzeige" pattern), and confirmed the acceptance
criteria's grep literally. No mistranslation, no awkward phrasing, no
scope creep, no terminology inconsistency found.

## Verified, live, not just read

- `grep -n "🍽️\|🥡\|Gemischt" web/help/de/sell.md` — only the held-sale
  historical note (line 52), not an active-feature paragraph. Matches
  the acceptance criteria exactly.
- `grep -rl "🍽️\|🥡\|Gemischt" web/help/de/` — only `sell.md`.
- `bash scripts/ci/guard-help-drift.sh` — ✓ green; the pre-existing `de`/
  `sell` baseline entry (tracked under ut-docs#1962/#2308/#2339) is
  unchanged in size, no new or stale drift introduced by this edit.
- `bash scripts/ci/guard-help-topics.sh` — ✓ green.
- `bash scripts/ci/guard-compliance-claims.sh` — ✓ green (343 files
  scanned, `web/help/**` included).
- `git diff --stat` — single file touched (`web/help/de/sell.md`, 3
  insertions / 3 deletions), no scope creep.

## Verdict

**Safe to merge** — a genuine content-drift fix (translation was
key-complete but false), independently reviewed with no findings, all
relevant guards green. `merge_method: "merge"`.
