# Review: help-breadcrumb follow-up (ut-docs#4891)

**Date:** 2026-10-24 · **Lane:** lane:cloud-41 · **Built by:** Sonnet subagent · **Reviewed by:** Opus (independent subagent, separate worktree) · fixes by the Opus orchestrator

## What shipped
- **German help:** the 4 baselined de mismatches are fixed in the prose. All 4 lines are deleted, so `help-breadcrumb-baseline.txt` is down to its header.
  - `backups.md` now says "Einstellungen → Backups". The de pack already has `settings.backup.title` = "Backups", so no pack change is needed.
  - `catalog.md`: "Katalogimport-Standardeinstellungen".
  - `users.md`: "Automatische Sperre".
  - `display.md` item 12: the missing last sentence (RTL pack auto-switch, "Einstellungen → Sprache") is now translated.
- **Guard (`checkhelpbreadcrumbs`):** a new explicit `extraSectionKeys` list covers the on-screen section names that don't have the `settings.*.title|.label` shape: `settings.all`, `settings.barcode.symbologies_title`, `tills.title` and `menulayout.title`. Each one is the key the Settings page renders. A primary key wins when two keys share a value, so all 47 existing mappings are unchanged (the reviewer diffed the map before and after). The table test `TestLabelMapExtraKeys` covers this.
- **English help:** "Settings → Data" now reads "Settings → Data management" and "Settings → Printer" reads "Settings → Receipt printer", matching the headings the till shows.
- **Translated help (de/tr/ar/fa):**
  - The Data breadcrumb now uses each locale's `settings.data.title`: Datenverwaltung, Veri yönetimi, إدارة البيانات, مدیریت داده‌ها.
  - 4 mismatches that were already on `main` and that the newly mapped keys exposed are fixed: ar multitill item 1 (الصناديق), and ar/fa/tr multitill item 13 (the `menulayout.title` value).

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | High | The first cut mapped "Tills" and "Hidden menu tiles" to `page.title.tills` / `page.title.hidden_menu_tiles`. Those keys only set the browser `<title>`, and in tr/ar/fa they differ from the section heading (tr Cihazlar vs Kasalar). The diff had rewritten correct prose to the wrong label in about 12 places. | **Fixed:** mapped to `tills.title` / `menulayout.title`, and the tr/ar/fa hunks are reverted. An automated reverse pass also changed a legitimate "more tills than devices" word in ar/fa multitill. A character-level diff against `origin/main` caught it, and those two files were restored. |
| 2 | Low | Till strings in en.json, such as `import.sample.settings_link` "Open Settings → Data", still say "Data". | Deferred to **ut-docs#4898**: a value change ripples into every locale and pack. |
| 3 | Low | The `settings.*_title` wildcard pulled card sub-headings in as breadcrumb targets. | **Fixed:** replaced by the explicit 4-key list. |
| 4 | Nit | de `display.md:32` phrasing. | **Fixed:** "wechselt die Kasse automatisch zu dieser Sprache". |
| 5 | Minor | The "primary beats extra" subtests also pass on the old code, because it ignores tier-2 keys. | Accepted. The "extra keys" and "extra tier" subtests do fail on `origin/main`'s `main.go`; the reviewer reverted and re-ran to confirm. |

## Verified beyond automated tests
- The reviewer reverted `main.go` to `origin/main`: `TestLabelMapExtraKeys` fails, and passes again once the change is restored.
- The guard passes in both setups:
  - With the de pack: 152 topics checked.
  - Without it (a `git ls-files` copy in a mktemp dir): 112 topics checked, de skipped with a notice.
- I checked every remaining tr/ar/fa change against `origin/main` at character level. Each one is a Data-management substitution or one of the 4 label fixes; nothing else changed.
- Gate after the last edit, all passing:
  - `gofmt -l scripts`: no output.
  - `go build ./...`
  - `go test ./scripts/ci/... ./internal/manual/... ./web/...`
  - `golangci-lint run ./scripts/ci/...`: 0 issues.
  - Guards: help-breadcrumbs (and its `_test.sh`), help-drift, help-topics, compliance, competitor, RTL, i18n, mail-addresses and ci-tests-sandboxed.
- No UI surface was touched (help prose and a CI guard only), so no screenshots are needed. No Go or e2e test asserts the renamed help text.

## Verdict
Safe to merge. Deferred: ut-docs#4898.
