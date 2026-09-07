# Code review: remove dead `PluginSummary.Locale` field (ut-docs#1064)

**Date:** 2026-09-06
**Branch:** `fix/1064-remove-dead-pluginsummary-locale`
**Card:** universaltill/ut-docs#1064
**Complexity:** easy

## What shipped

`marketplace.PluginSummary.Locale` (`internal/plugins/marketplace/client.go`)
was written once in the hand-rolled `UnmarshalJSON` and read nowhere in
production — real locale matching goes entirely through
`AvailableLocales []string`. Removed:

- The `Locale string` field from the `PluginSummary` struct.
- The corresponding `Locale string \`json:"locale"\`` decode target and its
  assignment (`p.Locale = w.Locale`) in `UnmarshalJSON`.
- The one test fixture referencing it (`client_test.go`), which set
  `Locale: "en-US"` but never asserted on it.

No behavior change: an unrecognized `"locale"` key in a JSON payload is
simply ignored on decode, same as any other now-unused wire field.

## Independent review

Spawned a fresh-context Sonnet subagent (easy-complexity routing —
different instance, not a different model, per the `reviewer` skill's
documented exception for `complexity:easy`). It did not just read the
diff; it re-ran the verification itself:

- Grepped the whole repo (not just the diff) for `.Locale`/`Locale:` and
  confirmed every other hit resolves to an unrelated type
  (`config.Locales`, `ListPluginsRequest.Locale`, `CatalogSnapshot.Locale`,
  page-local `basePluginSpec`/`installableLanguage` structs, and a
  separate `PluginSummary` type local to `scripts/mock-marketplace`).
- Cross-checked the server side in `ut-cloud`: the real proto
  `PluginSummary` message has only `AvailableLocales` (field 13) — no
  singular `locale` field at all, confirming this was already dead on the
  wire, not just unread locally.
- Checked `docs/market_place_openapi.yaml` for documentation drift: the
  `Plugin`/`PluginDetail` schema never documented a `locale` field on the
  plugin-summary object, so this removal introduces no new drift (a
  pre-existing, unrelated gap — the spec is also missing
  `available_locales` — was noted but is out of scope for this card).
- Ran `gofmt -l`, `go build ./...`, `go vet ./...`,
  `go test ./internal/plugins/marketplace/... ./internal/pages/...`,
  the full `go test ./...`, `golangci-lint run ./...`, and the
  `guard-data-access`/`guard-i18n`/`guard-compliance-claims` CI guards —
  all clean.
- Verified the Pact contract test
  (`tests/contract/marketplace_consumer_test.go`, `-tags contract`) and
  the cross-repo catalog fixture test
  (`internal/plugins/marketplace/catalog_contract_crossrepo_test.go`) are
  both untouched by and pass under this change.

**Verdict: SAFE TO MERGE, no findings.**

## Verified beyond automated tests

- Targeted + full `go test ./...`, `go vet ./...`, `gofmt -l .`, and
  `golangci-lint run ./...` run personally before handing off to review
  (all clean), then re-run independently by the review subagent with the
  same clean result.
- Every CI-blocking guard applicable to a Go-only, backend, non-UI change
  run locally: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-page-http-error.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`,
  `check-brand-assets.sh`, `guard-makefile-version.sh` — all pass (none
  are logically triggered by this diff, but running them costs nothing
  and confirms no accidental collateral change).
- No real client/shop name or secret-shaped literal in the diff (checked
  personally and by the independent reviewer).

## Deferred / out of scope

- `docs/market_place_openapi.yaml`'s `Plugin`/`PluginDetail` schema is
  missing `available_locales` (a pre-existing drift the review surfaced
  incidentally, unrelated to this card's own dead field) — not fixed
  here; worth its own card if it causes real confusion, but not filed
  separately as a card given how minor and pre-existing it is.

## Safe-to-merge

Yes. Merged via `merge_method: "merge"` (never squash/rebase — see the
`reviewer` skill's "Merge method" note, ut-docs#250) once CI is green.
