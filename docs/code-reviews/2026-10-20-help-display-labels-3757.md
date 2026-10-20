# Review — help names the Display labels the till shows (ut-docs#3757)

Date: 2026-10-20 · Lane: lane:cloud-41 · Built by: Sonnet · Reviewed by: Opus 5.5 (fresh context)

## What shipped

Localized help prose now names the labels the till actually renders:

- **tr** (`display.md`, `users.md`, `sell.md`): `Ayarlar → Görünüm` → `Ayarlar → Ekran`
  (`settings.display.title`; *Görünüm* is the Look & feel tile, `settings.cat.look.label`),
  suffixes fixed by vowel harmony (`Ekran'da`, `Ekran'daki`, `Ekran bölümü`), plus
  item 5's "Görünüm ayarlarından" → "Ekran ayarlarından".
- **fa** (`display.md`, `users.md`): `تنظیمات ← نمایش` → `تنظیمات ← نمایشگر`
  (`settings.display.title`; *نمایش* is `common.view`), plus "بخش نمایش" ×2 and
  "تنظیمات نمایش" in `display.md`.
- **de** (`display.md`, `users.md`): „Zum Betriebssystem-Fenster verlassen“ →
  „Zum Betriebssystem wechseln“ (the de pack's `settings.display.exit_to_os_title`),
  „Zum Betriebssystem verlassen.“ → „Betriebssystem verlassen.“ (`exit_to_os_success`).
- `web/help/img/manifest.json`: topic hashes for the changed fa/tr topics refreshed
  (sha256 of the markdown; screenshots and `surface_sha256` unchanged — prose only).

The stale de pack string (`settings.display.exit_to_os_help`, "derzeit ohne Funktion")
is fixed in ut-plugin-language-de (separate PR, v1.1.259).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | fa `display.md` item 11 still said "بخش نمایش" twice (the same phrase fixed in tr) | fixed |
| 2 | minor | tr/fa `display.md` item 5 ("Display settings" for the on-screen keyboard) still used the wrong label | fixed |
| 3 | minor | tr/fa `users.md` item 12 duplicates item 14; English wizard item untranslated (pre-existing) | deferred → ut-docs#4697 |
| 4 | nit | review record missing | this file |

Every other replaced spot was checked against the same numbered English item;
nothing was changed that names a different section (tr `display.md:14` tile list
correctly untouched; fa verb uses of نمایش untouched).

## Verified beyond automated tests

Built the till and served it from a scratch data dir; `GET /help/display` in tr, fa
and de returned 200 and rendered "Ayarlar → Ekran", "تنظیمات ← نمایشگر" and
„Zum Betriebssystem wechseln“. No screenshot change (prose-only), so none was
regenerated; not checked on a device.

Gate: `guard-docs-shots`, `guard-help-drift`, `guard-help-topics`, `guard-i18n`,
`guard-compliance-claims`, `guard-competitor-naming`, `guard-rtl-logical-css` green;
`go test ./internal/manual/... ./internal/httpx/... ./internal/pages/ -run 'Help|Manual'` ok.

Follow-up guard so this class is caught mechanically: ut-docs#4696.

**Verdict:** safe to merge.
