# Review: remove the dead locale keys left by #2698 (ut-docs#2710)

Date: 2026-11-04. Branch `chore/2710-dead-locale-keys` (uncommitted at review
time, forked from `origin/main` e0c5b9acc). Author model: Sonnet 5.5 Dev
agent; independent reviewer: Opus 5.5 (different model per
`reviewer/SKILL.md`). Companion pack PRs on the same branch name in
`ut-plugin-language-{de,es,pt}`.

## What shipped
- `web/locales/{en,ar,fa,tr}.json`: the three keys #2698 orphaned are gone —
  `tile_sheet.delete_item`, `tile_sheet.delete_item_confirm`,
  `elevation.summary.buttons_delete_item` (3 lines per file, 3843 keys each
  remain).
- `scripts/ci/i18n-baseline/key-meta-baseline.txt`: the same three entries
  pruned (shrink-only, required — see "Verified" below).
- No Go, template, help or docs change; `/api/buttons/delete-item` stays as
  the #2698 alias of remove-from-grid and already renders
  `elevation.summary.buttons_remove`.

**Pre-flight:** `web/i18n-meta/en.meta.json`: n/a (the three keys were
baseline-only — no `en.meta.json` entry existed). Every `web/locales/*.json`:
updated (en, ar, fa, tr — all four locales in the repo). Lang packs:
follow-up PRs prepared in all three repos `check-lang-pack-drift.sh`'s
`PACKS` lists (de, es, pt), reviewed in their own records, must merge after
this one (finding 1). `web/help/` topic + `make docs-shots`: n/a (keys had no
consumer, so nothing renders differently; `web/locales/**` is not in
`guard-docs-shots.sh`'s surface hash and the guard is green on the recorded
manifest). `reference/` page / `README.md`: n/a (no documented behaviour).
ADR: n/a. `e2e/tests-i18n-audit/allowlist.json`, `android/**/strings.xml`,
ut-cloud locales: n/a (none names these keys; checked). Review record: this
file. Follow-ups: the card's second half (finding 2).

## Verified (re-run personally, not taken from the dev notes)
- **No consumer anywhere.** `grep -r` for `tile_sheet.delete_item`,
  `tile_sheet.delete_item_confirm` and `elevation.summary.buttons_delete_item`
  across `universal-till`, `ut-plugin-language-{de,es,pt}`, `ut-cloud`,
  `ut-docs` and `ut-infra`: zero hits outside the locale files and the
  baseline this change edits. No dynamic construction either — every
  `T (printf …)` site in the repo builds keys under other prefixes
  (`shrinkage.reason.%s`, `subscription.plan.%s`, `permissions.action.%s`, …),
  the `buttons_api.go` elevation summaries are string literals, and no
  page-local `var T = {…}` map or `web/public/**/*.js` file names them.
  `/api/buttons/{remove-from-grid,delete-item}` both assert the
  `buttons_remove` wording in `internal/pages/buttons_hide_api_test.go`.
- **The baseline edit is required, not cosmetic.** Restoring only
  `key-meta-baseline.txt` from `HEAD` makes `guard-i18n-meta.sh` fail with
  three "is not in web/locales/en.json — remove the stale entry" errors;
  with the change it passes (486 described keys, 3357 baseline).
- **ar/fa/tr removal is correct.** `guard-i18n.sh` exits 0 either way but
  prints `orphan : tile_sheet.delete_item*` when a non-English locale keeps
  a key en.json lacks (verified by restoring `ar.json`), so dropping them
  keeps the orphan report clean. Code PRs still carry English only
  (ADR-0160 §2, #5478) — this is a deletion, not a translation.
- **No installed till regresses.** `git grep` over every `v0.34.*` and
  `v0.35.*` tag: no released build consumes the keys, so a pack update that
  drops them cannot blank a string on an older till (and extra keys in an
  installed pack/bundle are inert — `syncLocales` reads
  `map[string]string`).
- **JSON.** All four core locale files parse; no duplicate keys; 3843 keys
  each.
- Gates run: `guard-i18n.sh`, `guard-i18n-meta.sh` (+ `guard-i18n-meta_test.sh`),
  `guard-help-topics.sh`, `guard-help-drift.sh`, `guard-help-breadcrumbs.sh`,
  `guard-help-terms.sh`, `guard-compliance-claims.sh`,
  `guard-competitor-naming.sh`, `guard-mail-addresses.sh`,
  `guard-android-i18n.sh`, `guard-rtl-logical-css.sh`, `guard-docs-shots.sh`
  (surface `7d9729fe6c6a…`, unchanged), `check-lang-pack-drift.test.sh`;
  `check-lang-pack-drift.sh` against local `file://` mirrors of the three
  edited packs — "all known language packs are in sync with core", 3843/3843
  per pack. `go build ./...`; `go test ./web/... ./internal/httpx/...
  ./internal/config/... ./internal/msgfmt/... ./internal/ui/...
  ./internal/plugins/... ./scripts/... ./internal/pages/...` green. `gofmt`,
  `golangci-lint`: n/a (no Go file touched).

## Findings
| # | Sev | Finding | Disposition |
|---|-----|---------|-------------|
| 1 | Non-blocking (process; the card's own instruction is wrong) | ut-docs#2710 says "packs first or together so lang-pack-drift stays green". For a *removal* that is backwards, and no order is fully green. Measured both ways: an edited pack against core `main` fails its own `check-key-drift.sh` with **3 missing keys (new drift)**; an unedited pack against the edited core fails with **3 orphan keys**. So the pack PRs cannot go green before core merges, while core's own PR is unaffected (`lang-pack-drift` is advisory on `pull_request` and exits 0, emitting a `::warning::`). | **Accepted as a merge-order instruction:** merge **core first**, then the three pack PRs in the same cycle. The one `push`-to-`main` `lang-pack-drift` run in the window goes red on orphans (expected, same shape as the "brand-new key" rule in `reviewer/SKILL.md` step 4); clear it with a `workflow_dispatch` re-run once de, es and pt have landed. `lang-pack-drift` must stay a non-required check. |
| 2 | Non-blocking | The card's second half — translate the missing opening paragraph of "Rearranging the quick buttons" into de/ar/fa/tr `web/help/sell.md`, align de "Schnelltasten"/"Schnellwahltasten" and tr "Bitti"/"Tamam" in `till-designer.md` — is not in this diff. | **Deferred, and acceptable:** translated help now comes from Weblate (ADR-0160 §2, #5478 dropped non-English help/locale requirements from code PRs); `guard-help-drift.sh` is warning-only and that exact `sell` gap is already recorded as pre-existing drift tracked on ut-docs#1962, follow-up ut-docs#1973. `guard-help-terms.sh` and `guard-help-breadcrumbs.sh` are green, so neither wording item is a guard failure today. Do **not** close #2710 as done on this PR alone: keep it open for the second half (or re-card it onto the Weblate/#1973 track). |
| 3 | Non-blocking (info) | `scripts/check-version-bump.sh` in each pack reports "no shipped file changed" while the work is uncommitted. | Re-checked against a `git stash create` snapshot: de 1.1.291→1.1.292, es 1.1.280→1.1.281, pt 1.0.135→1.0.136, each exactly one patch above that pack's latest tag. Re-run after committing. |
| 4 | Non-blocking (info) | ADR-0160 §2's sync job into `universaltill/ut-translations` (Weblate's Git backing) does not exist in this repo's workflows yet. | Nothing to change by hand: English stays the source in `web/locales/en.json`, so the removal propagates when that job lands. |

No blocking findings. No secrets, no client/shop names, no money, auth or
offline-path code touched.

## Verdict
Safe to merge. Merge this PR first, then the three pack PRs in the same
cycle (finding 1); leave ut-docs#2710 open for its second half (finding 2).
