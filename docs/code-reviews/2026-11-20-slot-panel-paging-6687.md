# Review: slot panel paging follow-ups (ut-docs#6687)

Follow-ups accepted in the #4114 review (`2026-11-11-slot-panel-paging-4114.md`
L2, L3 and the two backlog lines). Built by a Sonnet Dev subagent from a
BA + Architect note; tested by a separate Sonnet Tester; reviewed by an
independent Opus subagent (lane:cloud-41, 2026-11-20).

## Change

- L2: whole-slot panel ids are the per-entry id (`pluginSlotEntryPanelID`),
  not the position, so a late answer for one entry cannot fill another
  entry's `-alert` after the slot reloads. The one-digit suffix stays valid
  in `pluginSlotPanelSuffixRe`, so a page opened before the upgrade keeps
  working.
- L3: panel paging navs carry a `<template class="plugin-view-paging-failed">`
  with the existing `plugin.view.page_failed` text; `app.js` copies it into
  the panel's alert on `htmx:sendError` / `htmx:timeout`. No new key, no JS
  string.
- POST and job poll accept a panel-shaped `HX-Target` only when it is
  `pluginSlotPanelOf(entry.Slot, target)`; otherwise 400 and the plugin is
  not asked (mirrors the paging GET).
- `hx-sync="#<target>:drop"` on the action form, form and chat-ask form.

## Findings (Opus review)

| # | Sev | Finding | Outcome |
|---|-----|---------|---------|
| 1 | medium | ut-docs `plugin-views.md` still said a slot panel shows paging without links, contradicting the new bullet | Fixed in the ut-docs half |
| 2 | low-med | `hx-sync` applies to every plugin view (full page, overlay), not only panels: a second submit while one runs is now dropped | Kept on purpose (no double submit on a till); documented in `plugin-views.md` |
| 3 | low | The global network banner still shows alongside the panel notice | Kept: Tester's 360px check shows the banner is static at the top of main and never covers the panel notice (`elementFromPoint` on the notice returns the notice) |
| 4 | low | No driven e2e for the L2 race itself or for hx-sync request counting (needs a seeded panel action) | Accepted: L2 covered by `TestContentSlotPanelIDsStableAcrossReorder_6687` + id-shape e2e; hx-sync by render test |
| 5 | low | e2e "panel ids belong to the entry" redundant; timeout test does not assert focus/clear | Accepted (abort test asserts the clear; same code path) |
| 6 | nit | afterRequest backstop at app.js never runs on htmx 1.9.12 | Accepted, harmless |

No high findings. No i18n, RTL, showModal, native-confirm, kiosk-isolation
or raw-SQL issue.

## Verification

- `go test ./internal/pages/ ./internal/pluginview/` pass (Tester and
  Reviewer each ran it); `go build ./...`, `go vet`, `golangci-lint` on both
  packages: 0 issues.
- Mutations (Tester): reverting each of stable ids, POST/poll binding,
  failure template, hx-sync, and the app.js handler turns a test red.
- e2e `plugin-slot-paging-4114.spec.ts`: 10/10. Visual at 360px (abort
  case): notice in a warn box above the table, no clipping or horizontal
  scroll, rows kept, focus on Next.
- Guards: i18n, rtl-logical-css, no-showmodal, no-native-confirm,
  help-topics, fixed-dates, docs-shots (surface hash refreshed; no
  screenshot changes: `Docs-Shots-Unchanged: true`).

- Pre-flight: help topic n/a (no visible change outside the failure notice); docs-shots manifest hash refreshed; en.json keys n/a (reuses `plugin.view.page_failed`); README n/a; ADR n/a (follows ADR-0121 §7); reference `ut-docs/reference/plugin-views.md` updated.

Verdict: approve.
