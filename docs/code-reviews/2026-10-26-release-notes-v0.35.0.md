# Review: release notes v0.35.0

- **Date:** 2026-10-26 · **Author:** Sonnet 5.5 (release lane, local session)
- **Covers:** owner-facing notes (en, de, ar, fa, tr) for everything merged since v0.34.19. The v0.34.20 notes already on main (never released, no tag existed) are renamed to v0.35.0 and extended: replacing a dead main till (confirmation on the join screen, "Replaced by" on the old till, never main again), never-reused receipt numbers of new tills, plus the existing restore-from-file, copy-off reminder, re-join keeps unsent sales, restore keeps cloud identity, desktop could-not-start page and phone menu items.
- **Left out on purpose:** the sales copy (mirror) work: not merged, shipped dark. Developer-only and test-only merges.

## Checks

- Every bullet verified against merged first-parent PRs since v0.34.19 and the locale strings (status.cloud_auth.replaced, setup.cloud.replace.*).
- No placeholders or angle brackets in the notes; no PR/card numbers.
- `guard-release-notes.sh v0.35.0` ok; `go test ./internal/releasenotes/...` ok.
- Translations written by the cycle model per ut-docs reference/translation.md, wording matched to each locale's till strings (Hauptkasse, الصندوق الرئيسي, صندوق اصلی, ana kasa).

## Verdict

Safe to merge.
