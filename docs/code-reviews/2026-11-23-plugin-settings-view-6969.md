# Review record: plugin settings read-only view, phase 1 (ut-docs#6969, epic #6967)

Scope: universal-till: `plugin_settings` check-in report (`internal/pluginsettingsreport`), manifest fields `secret_last4`/`report`, ADR-0082 guard allowance. Sibling PRs: ut-cloud ingest + API, ut-my-shop view, ut-docs contract.

## Findings (independent reviewer: Fable subagent; Tester round 1 and 2 in tester-notes.md)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | high | A URL credential (user, query, fragment) in a non-`endpoint` setting (webhook `endpoint_url`, AI `image_endpoint`, declared untyped) was reported and stored | fixed both sides (till `urlValueCarriesCredentials`, cloud vet), tests added |
| 2 | medium-high | `vetPluginSettings` did unbounded schema lookups per check-in (entries vetted before the 100-plugin cap) | fixed: truncate to the cap, `WellFormedID` first, budget of 100 lookups per check-in, no lookup for an entry with neither value nor last4 |
| 3 | medium | Till `printable` and cloud `Plain` disagreed: a Persian value with ZWNJ was sent and silently dropped | fixed: both allow U+200C/U+200D, reject other format characters and U+FFFD; contract updated |
| 4 | low-medium | A `locales/*.json` over 512 KiB failed the release scan gate | fixed: such a file is skipped for the settings strings, not a gate failure |
| 5 | low | `last4` stored without a schema check | fixed: vet drops it unless the schema secret sets `secret_last4` |
| 6 | low | Unknown reported version vets against the newest approved schema (contract said "reduced to set") | accepted; contract reworded |
| 7 | low | `configured` wrong for plugins whose credentials are untyped (e.g. tax-de fiskaly keys) | deferred: Backlog card (filed on ut-docs, see PR), cosmetic |
| 8 | low | Plugins list does one schema read per installed listing per page | deferred, same card (bounded by the page size) |
| 9 | note | Releases scanned before this have no `settings_locales`: labels show as keys until rescanned | deferred, needs a rescan/backfill card before shops see the page |
| 10 | nit | Import placed in the stdlib group in `cloudsync_wire.go` | fixed (docs-shots surface hash regenerated, trailer `Docs-Shots-Unchanged: true`) |

Checked clean by the reviewer: till and cloud key-name heuristics identical; a declared secret never gets a value on
the till; `value` next to `set` dropped; keys ASCII-only and matched exactly; wrong types drop only that section; the
read API and the my. parser refuse a value for a schema secret; tenant isolation (`appOwnedStore`, viewer row in the
access table, listing from the store's own `MerchantListings`, `Cache-Control: no-store`); React text nodes only (no
`dangerouslySetInnerHTML`, no href from reported data); check-in never fails because of this section (only the
pre-existing NUL-byte 400); no file write or cwd-relative path added; vector matches `Build`; caps agree (100/64/600/256).

Pre-flight: manifest field -> both mirrors (till `ManifestSetting`, ut-cloud `CanonicalSetting` + `pkg/manifest`) updated,
`docs/plugin_guidelines.md` updated, ADR-0002 n/a (no new type); min_pos_version constant `SettingsReportMinPOSVersion`
= 0.36.4 (latest tag v0.36.3, first release carrying the fields; `>=` so 0.37.0 also passes); new keys: English in all
10 ut-cloud locales + `english-only-until-weblate.txt` (ADR-0160, same as precedent), no till `en.json` key added;
my. UI -> ut-cloud locales updated, help en/de/tr updated in ut-my-shop; ADR index n/a (ADR-0214 is PR #6968);
docs-shots: only the surface hash moved, trailer `Docs-Shots-Unchanged: true`; review record: this file.

Verification re-run after the fixes: till `pluginsettingsreport`, `cloudsync -run PluginSettings`, `pages -run DeviceExtra`,
`guard-docs-shots.sh`; cloud `claims`, `downloads`, `signing` (POS_DIR mirror), `handlers -run PluginSettings`, go vet,
gofmt. New tests failed before the fixes only in the sense that the code paths did not exist (URL, last4, lookup count,
ZWNJ); the URL and last4 assertions were not run red against reverted code (accepted, small pure functions).

Verdict: approved after fixes. ADR-0214 / ADR-0213 amendment (PR #6968) reviewed: consistent with this implementation
(Decision 2 report, ADR-0082 named allowance, no second mechanism); NOT merged because ADR-0213 PR #6965 is still open.
