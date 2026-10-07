# Review: v0.30.22 release notes (en/de/tr/ar/fa)

Date: 2026-10-07 · Lane: lane:local · Written by: Claude Opus 5.5 · Reviewed by: Claude Fable 5.1 (independent subagent)

These notes cover the owner-facing changes merged since v0.30.21:
- Join-discovery notice for a till that finds the shop's main till (ut-docs#2721).
- "Name your shop" notice for placeholder shop names (ut-docs#3114).
- Plugins can't take over core pages; plugin pages are view-only (ut-docs#3786, #3789).
- Sell-screen buttons survive Menu and back (ut-docs#3807).
- Pair with a shop works (ut-docs#3754).
- Hidden menu tiles on an additional till stick through sync (ut-docs#2999).

Left out: the net-quantity snapshot field (only visible on the online manage page), the marketplace-proxy removal, docs/test merges.

## Findings (all fixed)

| # | Finding |
|---|---|
| 1 | Pairing "works again" was wrong — it never worked in a released till; now "now works", naming Settings → Pair with a shop. |
| 2 | "Name your shop" is not a one-time reminder: it shows to managers on every page except Sell until the shop is named. |
| 3 | The sell-screen buttons are "Scan with camera", "Identify by camera", "Keyboard" — used the real labels in every locale. |
| 4 | The join notice's ✕ stops the question on that till for good, and only managers see it. |
| 5 | Missing: the hidden-menu-tiles sync fix (#1730) is owner-visible — added under Fixed. |
| 6 | Per-locale labels: tr uses "Dükkân" for the shop-name notice; pairing headings use each locale's screen title; tile labels match `menulayout.title` (tr/ar/fa from web/locales, de from the de pack). |

`guard-release-notes.sh v0.30.22` and `go test ./internal/releasenotes/` pass.

## Verdict

Safe to merge, then dispatch `release.yml` (patch) — ships the p1 sell-screen fix (ut-docs#3807).
