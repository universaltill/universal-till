# Code review — document the device-profile selector in the manual (ut-docs#1302)

- **Date:** 2026-08-30
- **Branch:** `docs/1302-display-mode-manual`
- **Reviewer:** independent reviewer (fresh-context Sonnet subagent,
  `complexity:easy` per the model-routing table)
- **Verdict: SAFE TO MERGE.** No findings.

## What shipped

Follow-up from `ut-docs#1259`'s review (finding NB-4,
`docs/code-reviews/2026-08-30-self-order-mode-revoke-session.md`). Settings →
Display's device-profile selector (Register / Back office / Self-order kiosk,
`POST /api/settings/display-mode`) was never documented in the manual at all
— a pre-existing gap that NB-4 flagged as more pressing once #1259 gave the
selector new user-visible behaviour (switching to self-order now revokes the
acting screen's own session).

Added one new list item to `web/help/en/display.md` (and the equivalent,
translated item to `fa`/`ar`/`tr`) covering: what each of the three profiles
does, that the setting is per-till, that changing it always asks for a
manager PIN, and that only Self-order kiosk signs the acting screen out
(Register/Back office never do) — with the ut-docs#1301 clarification that
the revoke is scoped to the acting screen only, not other devices' sessions
on the same till. Renumbered the doc's remaining list items (was 15 items,
now 16) in all four locale files.

Docs-only change — no code, no template, no locale JSON string touched.

## Independent review findings

None. The reviewer cross-checked the new English prose against the actual
handler (`internal/pages/settings_page.go`'s `POST /api/settings/display-mode`)
and the relevant tests (`internal/pages/self_order_mode_test.go` —
`TestSelfOrderMode_RevokesActingSessionOnEntry`,
`TestSelfOrderMode_DoesNotRevokeOtherSessionsOnTheTill`,
`TestDisplayModeRegister_DoesNotRevokeActingSession`,
`TestSetDisplayMode_BackofficeWritesNoRevokeAudit`) and confirmed every claim
in the new copy matches actual behaviour. The `fa`/`ar`/`tr` translations
were checked sentence-by-sentence against the English and against each
locale's own `settings.display.mode_*` UI strings (exact terminology match)
and found faithful and complete. All four files were confirmed to still have
exactly 16 sequentially-numbered items with no gaps or duplicates after the
renumbering, and `git status --short` showed only the four intended files
touched.

## Verification performed

| Check | Result |
|---|---|
| `bash scripts/ci/guard-help-topics.sh` | pass (route coverage, all locales structurally complete) |
| `bash scripts/ci/guard-compliance-claims.sh` | pass (221 files scanned, no forbidden claims) |
| `go build ./...` | pass |
| `gofmt -l .` | empty |
| `bash scripts/ci/guard-docs-shots.sh` | pass (after `make docs-shots`, see below) |

No Go code, template, or locale-JSON string changed, so `guard-data-access`,
`guard-kiosk-engine`, `guard-i18n`, and `go test ./...` are not implicated by
this diff; not re-run for a docs-only change.

### CI finding, fixed

**`guard-docs-shots.sh` failed on the PR** (build check, first push): this
guard's manifest hashes each routed topic's markdown *content*, not just its
rendered pixels — `display.md` changing in all 4 locales meant its manifest
entries were stale even though no screen itself changed. Missed locally
before the first push (not run alongside the other two guards). Fixed:
ran `make docs-shots` (23 topics × 4 locales, all passed) and committed the
regenerated `manifest.json`. That same run also picked up two screenshots
(`ar/translations.png`, `tr/sell.png`) that had drifted from their pages'
current rendering — unrelated to this PR's own change (their manifest hash
entries are untouched by this diff) but refreshed as a side effect of the
full run; committed along with it, consistent with prior precedent
(`docs/code-reviews/2026-08-30-self-order-mode-revoke-session.md`, "Blocking
1"). Guard passes locally after the fix.

## Checked and found clean

- Terminology for the three profile names matches each locale's own
  `settings.display.mode_register`/`mode_backoffice`/`mode_self_order`
  strings verbatim.
- No compliance-certification outcome claim introduced (ADR-0040) — this is
  device/session UX copy, unrelated to fiscal compliance; guard confirms.
- List numbering in all four locale files is sequential 1–16, no gaps or
  duplicates.

## Merge

`merge_method: "merge"` (never squash/rebase — ut-docs#250), after CI is
green on the PR.
