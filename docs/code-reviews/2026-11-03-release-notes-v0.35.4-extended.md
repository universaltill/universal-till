# Review: release notes v0.35.4, extended to 2026-11-03

Date: 2026-11-03 · Docs-only change (web/release-notes/{en,de,ar,fa,tr}/v0.35.4.md) · Card ut-docs#5327

## What shipped

The v0.35.4 note (first written 2026-10-30) now covers every owner-visible
merge in `git log --first-parent v0.35.3..origin/main` (65 merges, up to
b24c9af), not only the first third. Date moved to 2026-11-03. 18 bullets
(10 Improved, 8 Fixed), identical structure in all five locales.

**Added:** pairing requests pushed to the Tills page; confirm before
re-pairing a till already connected to a shop; Staff & security links to
Users and Permissions; self-order cart photos; Android system-camera
photo; list-first Settings on an upright tablet; warning-text contrast;
one grouped screen-reader/keyboard bullet; confirm/PIN prompt
Escape/Cancel/in-flight fixes; PIN prompt with the on-screen keyboard;
Settings deep links under the phone top bar; Quick Buttons refused-move
reason on the row. The sale-screen and plugin-download bullets were
extended with the later tender/Total and replaced/moved/no-owner merges.

**Left out:** CI/release/guard, e2e/test-only and README merges; the dead
install-modal key removal; the AI assistant settings/catalog tools (their
plugin PRs are unmerged, so owners can't use them — RELEASING.md,
ut-docs#4219); the sales-copy stack and the till move into a my. store
(both shipped dark); plugin chat focus (no plugin ships a chat view yet,
ut-docs#2833 open); single-locale help wording fixes.

## Review

Independent review by a different model (Opus; notes drafted by Sonnet).
Every added bullet was spot-checked against its merge; the five flagged
features (#2284, #2298, #2267, #2272, #2227 in universal-till) were confirmed
reachable with no off flag.

| # | Sev | Finding | Status |
|---|---|---|---|
| M1 | med | de: item editor tab is „Extras“ in the German manual, not „Modifikatoren“ | fixed |
| M2 | med | fa: Cancel is «انصراف» (`common.cancel`), not «لغو» | fixed |
| M3 | med | Android photo bullet overstated: Remove background can come first; app also runs on phones | fixed (all locales) |
| L1 | low | self-order cart: a line without a picture shows an empty slot; a changed photo shows on the next refresh, not live | fixed (all locales) |
| L2 | low | tr/ar "not set" wording didn't match `plugins.settings.secret_empty` | fixed |
| L3 | low | de: page is „Aktionen“ | fixed |
| L4 | low | ar: awkward phrasing in the confirm/PIN bullet | fixed |
| L5 | low | "old hint is cleared" vague for owners | fixed (all locales) |

## Verification

- `go test ./internal/releasenotes/... ./web/...` — ok (incl. TestBuiltin_NotesAreOwnerLanguage).
- `guard-release-notes.sh v0.35.4` (+ `--header`), `guard-release-notes_test.sh`, `guard-compliance-claims.sh`, `guard-competitor-naming.sh`, `guard-mail-addresses.sh` — ok.
- `scripts/release-notes-bundle`: v0.35.4 present in all five locales, 10+8 bullets each.
- Driven run (Tester): till built as 0.35.4 with `e2e/seed_release_notes`, Settings → About in en/ar/fa at 1280×900 — v0.35.4 first, 18 bold titles, no raw `**`, `dir=rtl` for ar/fa; en and ar screenshots inspected.

Pre-flight: review record — updated (this file); `web/help/` topic — n/a (release notes only; help gaps from earlier merges deferred → Backlog cards filed at close-out); en.json keys — n/a; README — n/a; ADR — n/a; release — follows merge (release.yml bump=patch).

Verdict: safe to merge.
