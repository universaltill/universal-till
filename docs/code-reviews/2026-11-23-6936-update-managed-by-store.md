# Review: store builds say where updates come from (ut-docs#6936)

Date: 2026-11-23. Complexity: easy, one review pass.
Author model: Opus 5.5. Reviewer: Sonnet 5.5 (independent, inline, fresh read
of the uncommitted diff on `feat/6936-update-managed-by-store`).
Verdict: **approve**, no code changes by the Reviewer.

Pre-flight: user-visible behaviour changed (Settings → Software update on
Play / App Store builds) and the `web/help/en/updates.md` topic is updated in
the same branch; `help/img/manifest.json` surface hash re-pinned
(`guard-docs-shots.sh` green). New `en.json` keys are English-only with
`en.meta.json` metadata (ADR-0160). `docs/reference/` n/a (no contract, API or
setting changed). No ADR needed: implements ADR-0194 §2.3 / ut-docs#6388 as
decided. No compliance, competitor, plan/price or plugin/manifest surface.

## What shipped

- `web/ui/pages/settings.html`: the `{{ if inappupdateallowed }}` block of the
  Software update card gains an `{{ else }}` that renders one muted line,
  `data-testid="update-managed-by-store"`: "Updates for this app come from
  Google Play" when `storechannel` is `play`, otherwise (appstore) "...the App
  Store". `inappupdateallowed` is false only on the two store channels, so
  sideload/direct take the existing branch, byte for byte unchanged.
- `web/locales/en.json` + `web/i18n-meta/en.meta.json`:
  `settings.update.managed_by_store`, `settings.update.managed_by_app_store`
  (`review: standard`, `max_length: 70`, no placeholders).
- `web/help/en/updates.md`: store-build paragraph names the new line.
- `internal/pages/store_channel_6388_test.go`:
  `TestStoreChannel_UpdateManagedByStoreBanner`, table over all four channels.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Nit | New en.json keys sit before `settings.update.android.*`, so the file's alphabetical order is off by one block. No guard checks order. | Accepted, no change (would churn the diff for no gate). |
| 2 | Info | The inner `{{ else }}` treats every non-play store channel as App Store. Correct today (`InAppUpdateAllowed` is false only for play and appstore, and the test covers all four channels); a future third store channel would show the App Store text. | Accepted: the test iterates `allChannels`, so a new channel fails loudly. |
| 3 | Info | Text is plain `T` output in a `<p>` (html/template escaping), no placeholders, no modal, no confirm, no CSS added (RTL n/a), no hardcoded strings. | Clean. |
| 4 | Info | Line shows for non-managers too (it sits outside the `isManager` gate, like the heading and version), which matches the card ("heading + version" visible to all). | Accepted. |

## Verification

- TDD claim re-verified inline: with `settings.html` reverted to
  `origin/main`, `TestStoreChannel_UpdateManagedByStoreBanner/play` and
  `/appstore` FAIL ("missing managed-by-store line ..."); direct/sideload
  subtests pass (unchanged behaviour). Restored with
  `git checkout HEAD -- web/ui/pages/settings.html`; test passes again.
- `go build ./...` PASS.
- `go test ./internal/pages/ -run 'StoreChannel|6388|Settings' -count=1` PASS.
- `scripts/ci/guard-i18n.sh` PASS (no missing keys, no duplicates).
- `scripts/ci/guard-i18n-meta.sh` PASS.
- `scripts/ci/guard-help-topics.sh` PASS (non-blocking warnings are
  pre-existing missing translations).
- `scripts/ci/guard-help-breadcrumbs.sh`, `guard-help-terms.sh` PASS.
- `scripts/ci/guard-rtl-logical-css.sh` PASS.
- `scripts/ci/guard-compliance-claims.sh` PASS.
- `scripts/ci/guard-docs-shots.sh` PASS (surface hash fresh).
- `git diff --check` clean. Not run: full `go test ./...`, golangci-lint
  (no Go source change beyond a test), webkit/e2e.

## Deferred

None.
