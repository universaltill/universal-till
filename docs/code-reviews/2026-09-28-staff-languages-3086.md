# Review: Languages shown to staff (ut-docs#3086)

**Date:** 2026-09-28 · **Branch:** `fix/3086-staff-languages` · **Built by:** Opus 5.5 (orchestrator, inline) · **Reviewed by:** Fable (independent subagent)

## What shipped
Product owner: the ☰ Menu's language row showed every installed language. It now shows only the languages a manager chose.

- **Setting** `store.staff_locales` (comma-separated locale codes). It is shop-wide (`store.` prefix), so an additional till sends it to the main till through `saveShopSettings`.
- **Read side** `httpx.StaffLocalesFor`: installed entries only, in installed order, always plus the shop's default language (regional tags match the base code: `de-DE` → `de`). Unset means the default language plus English. Shops set up before this change get that without any write.
- **Settings card** `#settings-staff-languages`: a checkbox for every installed language. The default is checked and disabled, and a hidden input carries it. Registered in `uislot.CoreSettings` (after Language), so it's in the two-pane sidebar.
- **`POST /api/settings/staff-languages`**: `settings` permission, with in-place PIN elevation for anyone without it. It refuses an unknown language, an empty list, or a list without the default (400, translated). Audited as `staff_languages_changed`. On the demo allow-list like the other settings.
- **Menu:** the row lists `staffLocales` and is hidden when only one is left. A `?lang=` link to an unlisted language still switches that browser but isn't added.
- Unchanged: the Settings default-language picker and the setup wizard (every installed language).
- i18n: 7 keys in en/ar/fa/tr. Manual: `display.md` step 1 in en/de/ar/fa/tr. docs-shots regenerated.

## Findings (Fable)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (process) | de/es packs lack the 7 new keys; `lang-pack-drift` blocks on `main` | Pack PRs land in the same cycle, right after core merges (brand-new keys can't land in a pack first) |
| 2 | minor | Hidden-input test assertion also matched the checkbox | Fixed: asserts `type="hidden"` |
| 3 | minor | Handler matched exact case, but the read side is `EqualFold` + base tag | Fixed: handler uses the exported `httpx.MatchLocale`; test covers `EN` / `tr-TR` |
| 4 | minor | The main till's `/api/sync/settings/apply` doesn't validate the value | Accepted: every till filters on read (uninstalled codes dropped, default re-added), same class as `shop.type` |
| 5 | minor | A non-400 error shows the generic banner, not the server text | Accepted: same as the shop-type card |
| 6 | minor (UX) | One-language shop plus a stale `?lang=` cookie leaves no in-menu way back | Accepted per AC; Settings → Language re-apply retires overrides (#2135) |
| 7 | nit | `ResolveLocale` before validation could set a cookie on a 400 | Fixed: `RequestLocale` |
| 8 | nit | Checkbox group had no fieldset/legend | Fixed: `<fieldset>` with visually-hidden legend |
| 9 | nit | Test mutates the global default locale | Accepted: `t.Cleanup` restores it; the package isn't parallel |
| 10 | nit | The e2e reset assumed an `en` default silently | Fixed: afterEach asserts 204 |

## Verified beyond unit tests
- The reviewer re-verified TDD: with `menu.html` + `menu_page.go` reverted, the three `MenuLanguageRow` tests fail as claimed and pass once restored.
- e2e `staff-languages-3086.spec.ts`: ticks Türkçe in Settings, saves, and the Menu shows English + Türkçe only; one language hides the row.
- Screenshots looked at: the Settings card and the Menu row, en and fa (RTL), at 1024×600 and 360px. No clipping, touch targets ≥ 44px, default greyed with a label.
- Not verified on real touch hardware (emulated viewport only). The change adds no pointer handlers.

## Gate
`gofmt` clean, `go build`, `go test ./...` all ok, `golangci-lint` 0 issues. All `ci.yml` build-job guards pass except, locally, `shellcheck` (binary not installed) and `guard-deadcode-baseline` (`internal/logging` entries predating this branch, no GTK headers here); neither touches this diff.

**Verdict:** safe to merge once the de/es pack PRs follow.
