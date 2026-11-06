# 2026-11-04 — Till names my.'s real pairing buttons (ut-docs#5489)

**What shipped.** The till told merchants to choose "Add or re-pair a till" in the shop's cloud account. my. has no such button; it shows "Pair a till" (`manage_app.till.pair`) and "Pair again" (`manage_app.till.pair_again`). Because the wording changed, the help paragraph moved to a new key `settings.enrol.pair_help_2` (with meta, `review: standard`); `settings.enrol.pair_help` is removed from en/tr/ar/fa and from the shrink-only `key-meta-baseline.txt`. `settings.html` uses the new key. The manual (`web/help/{en,de,tr,ar,fa}/claim.md` step 1) names my.'s labels, the translated topics with ut-cloud's own translations of those two keys. `make docs-shots` regenerated `web/help/img/manifest.json` only (no PNG shows this text). Comments in `internal/enroll/pair.go` / `pair_test.go` now name the real labels and the real till path (Settings → Till registration → Pair with a shop).

**Process.** BA, Architect, Dev (Sonnet), Tester (Sonnet), independent review (Opus 5.5).

**Tests.** New `TestSettingsPage_PairHelpNamesCloudButtons` (`internal/pages/settings_enrol_pair_test.go`): fails with the change reverted (3 assertions), passes with it. Re-verified by both the Tester and the reviewer in a throwaway worktree.

**Extra verification.** Driven run: `/settings#registration` in Chromium shows "Pair a till" / "Pair again"; `?lang=de`/`tr` fall back to English, no raw key; `/help/claim` step 1 correct in en and de. Every quoted label proofread against ut-cloud `locales/*.json`.

**Findings.**
1. minor — `pair.go:26`, `pair_test.go:19` still said "Settings → Cloud → Pair with a shop". Fixed.
2. minor — ADR errata placement (ut-docs PR). Fixed there with inline pointers.
3. nit — the test asserts on the whole page body, not the paragraph. Accepted: no other "Pair again" text exists on /settings today; the test also asserts the retired label is gone.
4. nit — "already listed" for "Pair again": my. shows it on a till in its list (incl. removed tills). Accepted.
5. info — pt pack uses "Associar" for the till's own button while my. pt says "Emparelhar" → Backlog card.
6. info — tr/ar/fa lose their translated paragraph until Weblate fills the new key (ADR-0160: code PRs add English only).

**Gate.** gofmt clean, go build, golangci-lint 0 issues, go vet; guard-i18n, guard-i18n-meta, guard-help-topics, guard-help-drift, guard-help-breadcrumbs, guard-help-terms, run-docs-shots-guard, compliance, competitor, mail, rtl all exit 0. `go test ./...` green except `internal/plugins` timing out at 600s in this container (not touched; CI is the arbiter).

Pre-flight: help topic `updated` (claim.md ×5 + docs-shots manifest); `en.meta.json` `updated`; `web/locales/*.json` `updated`; lang packs de/es/pt `updated` (follow-up PRs, same cycle); `reference/` + ADR errata `updated` (ut-docs PR); README `n/a` (no mention); ut-cloud `pair_steps` path and comments `deferred → #3184 / ut-cloud#799`.

**Verdict.** Merge.
