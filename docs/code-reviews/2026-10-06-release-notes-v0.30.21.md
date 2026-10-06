# Review: v0.30.21 release notes (en/de/tr/ar/fa)

Date: 2026-10-06 · Lane: lane:local · Written by: Claude Opus 5.5 · Reviewed by: Claude Fable 5.1 (independent subagent)

These notes cover the owner-facing changes merged since v0.30.20:
- Windows fullscreen/kiosk and start-at-login (ut-docs#610). New installs only; an update keeps the owner's choice.
- Plugins are told when a customer is erased (ut-docs#3435).

Docs, test and CI merges are left out.

## Findings

All were UI-label mismatches against what the till actually shows, and all are fixed:

| Locale | Finding |
|---|---|
| en | "Full screen" → **Fullscreen** (the option's label) |
| de | „Zum Betriebssystem-Fenster verlassen“ → **„Zum Betriebssystem wechseln“** (the de pack's label) |
| tr | Ayarlar → Görünüm (that's the Look & feel tile) → **Ayarlar → Ekran** |
| fa | تنظیمات ← نمایش → **نمایشگر** |
| ar | "جهاز نقطة البيع" → **"صندوق"**, consistent with the v0.30.20 note |

The reviewer confirmed the facts, the completeness of each translation and that the notes contain no jargon. The same label drift also exists in the existing help pages (de/tr/fa `display.md`, plus a stale de pack string). That is outside this change and filed as ut-docs#3757.

`guard-release-notes.sh v0.30.21` passes, and so does `go test ./internal/releasenotes/`.

## Verdict

Safe to merge, then dispatch `release.yml` (patch).
