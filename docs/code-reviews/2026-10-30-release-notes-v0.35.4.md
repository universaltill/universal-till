# Review: release notes v0.35.4

Date: 2026-10-30 · Docs-only change (web/release-notes/{en,de,ar,fa,tr}/v0.35.4.md)

## Scope check

The notes cover `git log --first-parent v0.35.3..origin/main`, plus #2240 (card 2918, the landscape-phone sale screen), which merges before this PR. Each item was read against its merge commit and review record.

**Included:**
- the LAN connection key is kept out of DB backups and rotated (5172, 5198), as one bullet;
- the phone-card status stays visible (4583);
- the sale screen on a sideways phone (2918);
- a main-till restore keeps joined tills (5203);
- restoring and rejoining an additional till (5162, 5177, 5185), as one bullet;
- translated plugin-download refusal reasons (3541).

**Excluded** (CI and release pipeline only, nothing the owner sees): 5211, 5209, 5201, 5205, 5207, 5206, 5200, 5175, and the Weblate README badge.

## Checks

- No "New" group, because no new feature shipped. Improved/Fixed headings match v0.35.3 in every locale.
- No PR or card numbers, and no internal jargon ("bearer", "replica" and "snapshot" are avoided).
  - `go test ./internal/... -run Notes` passes, including TestBuiltin_NotesAreOwnerLanguage.
  - `guard-release-notes.sh v0.35.4` passes.
- Translations were drafted by a Sonnet subagent and read by the cycle (Opus).
  - Terminology follows v0.35.3 and the locale packs.
  - The German "Der Hauptkasse erneut beitreten" is its own wording (core has no de.json).
  - The Arabic rejoin label reuses `sync.link.rejoin` ("الصندوق الأساسي").

Verdict: safe to merge.
