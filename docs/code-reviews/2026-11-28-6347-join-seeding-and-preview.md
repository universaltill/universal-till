# Review: join seeding, boot theme persistence, preview hidden-menu split (ut-docs#6347, epic #6332)

Reviewer: independent Opus 5.5 review (Sonnet-built code) plus the orchestrator's own
verification. One combined review for universal-till, ut-cloud and ut-my-shop.
Pre-flight: `web/help` topic choose-shop-type + docs-shots updated (8 PNGs, manifest); README n/a (no claim changed);
`en.json` + `en.meta.json` updated (English only, ADR-0160); my./partner. UI n/a (no UI change, test only);
ADR/reference n/a (no ADR change); plugin surfaces n/a; follow-ups deferred -> cards listed below.

## What shipped
- Generator and ut-cloud/my. never flag `store. shop. payments. payment. fiscal. tax. auth. security.` keys as rarely needed; generator `MkdirAll`.
- `seedJoinedTillTemplate` (boot + 30 s reconcile tick): a joined till seeds only its per-till theme (`ApplyPerTill`).
- Boot writers (`SaveRuntimeConfig`, `common.SaveBootState`) no longer persist the config-default theme when no row exists.
- Preview lists hidden menu tiles apart from hidden settings cards (new key `group_hidden_menu`).
- `templateStateInterval` const -> var (test seam).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | High | A real joined till restores the main till's full DB: `theme` and `template.theme_seeded` rows are NOT stripped by the join snapshot or `ApplyReplicaIdentity` (`internal/db/replica.go`), and the wizard's `SaveState` (`setup_page.go:708`) writes the theme before the template applies. So on a real join (and a fresh main wizard) the theme row is already set and `ApplyPerTill` skips: the seed is a no-op in production. Verified by reading the code. The new tests start from a DB with no theme row, which a real joined till never has. | **Deferred -> card.** Fix needs a product call (a joined till stops inheriting the main till's look; wizard ordering). Dormant today: no shipped template seeds a theme (finding 3), so no user-visible regression. #6347 stays open. |
| 2 | Med | A seeded theme is written to the DB only; `CurrentState().Theme` and `httpx.InitTheme` keep the config theme until restart. | **Deferred -> same card** (needs `ApplyPerTill` to report a write, then re-derive). |
| 3 | Med | No shipped template seeds a theme; the join seed plus the boot-persistence change have no visible effect yet. `UT_THEME` stays authoritative while no row exists. | Accepted, recorded. |
| 4 | Med | The till still demotes card `settings-idle-lock` for market_stall (`templates/market_stall.json`, reference table row 24 says M(solo): A) while the artefact no longer lists `auth.idle_lock_minutes`; so the till shows it under Advanced and my. shows it as normal. ADR-0190 section 5 protects keys from template *seeds*; idle-lock is not in the fixed-class list. | **Decision: keep, documented.** The deny list is the conservative my. side; the till demotion matches the reference. Generator comment corrected (it overstated the ADR). Owner question carded: also deny `invoice.`, `marketplace.`, `kiosk.payment_mode`? |
| 5a | Low | TOCTOU: promotion between the two `tillFollowsMain` checks could run the full main-till `Apply` from the tick. | **Fixed:** `seedJoinedTillTemplate` calls `ApplyPerTill` directly. |
| 5b | Low | Tick read-then-write could overwrite an owner theme save in a ms window. | Accepted (negligible). |
| 5c | Nit | Marker only written on a seed, so ~4 reads per 30 s forever on a no-seed till. | Accepted. |
| 6a | Low | Preview test sliced `body[im:is]` across shown groups, so a misplaced card could pass. | **Fixed:** `between(..., "</ul>")`. |
| 6b | Nit | `TillSettingsEditor` test passes before and after (pins the existing list). | Accepted, as acknowledged. |
| 6c | Nit | `templateStateInterval` shared global; `-race` on the target set clean. | Accepted. |
| 7 | Nit | `group_hidden_menu` text contains a `>` breadcrumb. | **Fixed:** RTL/keep-names hint in the meta description. |
| 8 | Nit | `SaveRuntimeConfig` used `== ""`, `SaveBootState` used `TrimSpace`. | **Fixed:** both `TrimSpace`. |

Other readers of the theme row with no row (checked): `LoadState` (`get(KeyTheme, cfg.Theme)`), `LoadRuntimeConfig` (`!= ""`), personal display, `ResolvedTheme`, mobile/cmd: fine. Theme is per-till and excluded from admin sync (`ThemeSettingsKey`, #2783), so nothing syncs an empty theme. Owner choices cannot be overwritten on boot: an existing row is rewritten as before, handlers keep `SaveState`, and `ApplyPerTill` never overwrites a set value. The lists are identical in generator, ut-cloud and my. (JSON byte-identical in ut-cloud). Merge universal-till first, then ut-cloud (drift guard).

## Extra verification (done by me, in a scratch worktree)
- Reverted `SaveBootState` -> `SaveState` in `init.go`: `TestInitBootSeedsJoinedTillTheme` FAILS (`theme after boot = "default", want "dark"`); restored, passes.
- Reverted the `SaveRuntimeConfig` theme skip: `internal/settings` runtime tests FAIL; restored, pass.
- Removed both `seedJoinedTillTemplate` wiring calls (boot and tick): `TestInitBootSeedsJoinedTillTheme` (`theme after boot = ""`) and `TestTemplateStateReconcileTickSeedsJoinedTillTheme` (`theme after reconcile tick = ""`) FAIL; restored.
- `go test ./...` once on the merged branch (pre-fix commit): all packages ok except `mobile` `TestStart_ListensOnAllInterfacesButReturnsLoopbackAddress` (host LAN IP, known noise, also fails on origin/main). `golangci-lint run ./...`: 0 issues. After the review fixes: gofmt, vet, golangci-lint on pages/settings/cmd, and the targeted tests (settings, gen-settings-registry, pages Join/TemplateState/ShopTypePreview/InitBoot/ShopTemplate) pass; guard-i18n-meta passes.
- Reviewer ran `-race` on the join/preview set: clean. It saw `TestSetupGBJoin_AutoInstallsEligibleTaxPlugin` fail once under `-race`, passing alone, code path untouched (flaky, not this change).

## Verdict
Approve the commits for merge (no regression; fixes applied; dormant feature). **#6347 is NOT done:** findings 1 and 2 mean the per-till seed does not take effect on a real join. Tracked on the follow-up card; do not mark #6347 Done. No real shop names or secrets in the diff.

## Deferred
- Card A (Architect/owner decision): joined till must not inherit `theme` / `template.theme_seeded` from the snapshot (`ApplyReplicaIdentity`); wizard writes theme before the template applies; applied seed must update live state; tests from a snapshot-shaped DB asserting `CurrentState().Theme`.
- Card B (owner): widen the never-rarely list to `invoice.`, `marketplace.`, `kiosk.payment_mode`? Decide whether the till's idle-lock card demotion should follow my.

## Addendum (2026-11-28): findings 1 and 2 closed
Decision (owner): the theme is per till and never synced (ut-docs#2783, reference/settings-templates-by-shop-type.md 1.3), so a joined till must not inherit the main till's theme or `template.theme_seeded`.
- **Finding 1, fixed at the root:** `db.DeletePerTillLook` (exact keys `db.PerTillLookKeys` = `theme`, `template.theme_seeded`) now runs in `ApplyReplicaIdentity` (sink, covers every join/re-join that stages an identity with the snapshot) and in `RedactedJoinSnapshot` (source copy). A plain backup restore (no identity staged) keeps its own theme on purpose: it is this till's own data. `TestPerTillLookKeysMatchShopTemplate` pins the marker name (db cannot import it for the code).
- **Wizard ordering:** not on the join path. A joined till restores a finished setup, so the first-run wizard's `SaveState` never runs on it; a fresh main till's wizard sets its own theme and applies a template with its own full `Apply`. No change.
- **Finding 2, fixed:** `seedJoinedTillTemplate` republishes a newly seeded theme to `CurrentState().Theme` and `httpx.InitTheme`, so it is effective without a restart (boot and 30 s tick).
- **Tests (TDD, fail-before shown):** `TestApplyReplicaIdentityDropsMainTillTheme`, `TestRedactedJoinSnapshotDropsTheme` (theme/marker stayed `amber`); `TestInitBootJoinedTillFromMainSnapshotEndsWithTemplateTheme` starts from main's DB (theme=amber + marker) restored via `ApplyReplicaIdentity` and failed with stored theme "amber" with the sink fix off, and `CurrentState().Theme` "default" with the live update off; the tick test now also asserts `CurrentState().Theme`.
- **Gate:** gofmt, vet, golangci-lint (0 issues), `go test` for `internal/db`, `internal/pages`, `internal/shoptemplate`, `internal/settings` all pass.
- docs-shots: no screen changed; nothing regenerated. Finding 3 (no shipped template seeds a theme) is unchanged.
