# Review — AI plugin image_model hint copy (ut-docs#4448)

**Change:** `plugins.settings.ai.image_model_unsupported` said "…so
background removal stays off." Since ut-docs#4137 an unsupported model only
makes `bgremove.newCutter` return nil; `aiCutoutOffered` then falls back to
the built-in on-device model (`cutoutEngineBuiltin`), as the plugins help
page already says. The key is reworded in en/ar/fa/tr to match its sibling
`image_endpoint_invalid` ("…the till ignores it and keeps using its built-in
background removal…"). The stale `settingNoticeKey` doc comment is fixed too.
Placeholders are unchanged, so the key keeps its name. Pack follow-ups:
`ut-plugin-language-{de,es,pt}` branch `i18n/4448-image-model-hint`
(locale line + manifest patch bump).

New test `TestAIImageModelUnsupportedHint_SaysBuiltinStaysOn`
(`internal/pages/plugin_settings_page_test.go`): checks that an unsupported
model leaves the service unused, that en says "built-in background removal"
and not "stays off", and that ar/fa/tr reuse their sibling's clause.

## Review

Independent Fable review (author: Opus 5.5).

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The hint shows for any off-list model, whatever the provider/endpoint. With an empty `image_endpoint` and a valid `UT_AI_IMAGE_ENDPOINT` env override on the host, the till uses the env-configured service, not built-in removal. "The till ignores it" holds in every case. | Accepted. This is an operator deployment path, not a settings-form state; the old copy was wrong there too. |
| 2 | nit | `settingNoticeKey` comment still said resolution "turns background removal off" | **Fixed** |
| 3 | nit | Key still in the shrink-only `key-meta-baseline.txt` with no metadata | Not changed (not this card's scope) |

Translations checked by the reviewer: each reuses its locale's sibling
clause and usual till word; pronouns adapted for "model" (de *es*, es *lo*,
pt *-o*).

## Verified

- TDD (reviewer, in a separate worktree): with `web/locales` at the pre-fix
  commit the new test fails for en/ar/fa/tr; restored, it passes.
- `gofmt`, `go build ./...`, `go vet`, full `go test ./...`,
  `golangci-lint run ./internal/pages/...` (0 issues).
- Guards: i18n, i18n-meta, compliance-claims, competitor-naming,
  help-drift, help-topics, mail-addresses, rtl-logical-css, core-neutral.
- Packs: `validate.sh` and `check-key-drift.sh` pass for de/es/pt
  (3447/3447, 0 drift).
- Not driven in a browser: copy-only change to an existing hint whose
  rendering `TestPluginSettingsPage_GET_AIImageModelUnsupportedHint` already
  covers through the real handler.

**Verdict:** safe to merge.
