# Review — AI plugin image_endpoint hint (ut-docs#4095)

**Change:** the AI plugin's `image_endpoint` setting is plain text on
purpose — `type: "endpoint"` would grant the WASM plugin `http:lan` egress
to the rembg host — so the settings page never validated it on save. Core
checks it when used (`bgremove.newCutter` → `plugins.ValidEndpointURL`), so
a bad value was safe but silent: an operator who typed `192.168.1.20:7000`
(no scheme) got a 200, no hint, and the till quietly kept using its
built-in background removal instead of their own service.

`settingNoticeKey` (`internal/pages/plugin_settings_page.go`) gains an
`image_endpoint` case — third adopter after `api_key` and `image_model` —
returning `plugins.settings.ai.image_endpoint_invalid` when the trimmed
value is non-empty and not a valid http(s) URL. Plain muted paragraph above
the input, never a modal. New key in en/ar/fa/tr; one sentence added to
`web/help/{en,de,ar,fa,tr}/plugins.md`. de/es/pt pack follow-ups:
`ut-plugin-language-{de,es,pt}` branch `i18n/4095-image-endpoint-hint`.

## Review

Independent Fable review (author: Opus 5.5). It ran build, vet, the
affected tests, the full `internal/pages` + `internal/bgremove` packages
and the i18n/help/compliance guards, and traced both unwrap paths (page:
`DecodeMapSettingValue` + TrimSpace; resolver: `json.Unmarshal` + TrimSpace)
to confirm that for every value the settings form can store, the hint shows
exactly when the till ignores the endpoint and `cutoutEngine` falls back to
`builtin` — so the copy is accurate.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | ar copy said "جهاز البيع" for the till; ar.json uses "الصندوق" everywhere else, including the sibling key | **Fixed** |
| 2 | nit | Page and resolver unwrap differently for non-string JSON in `value_json` (e.g. bare `null`) — not reachable from the form or `settings_set`; already documented on `unwrapSettingValue` | Accepted, pre-existing |
| 3 | nit | Negative test cases didn't assert the input still renders | **Fixed** |
| 4 | — | File write without `os.MkdirAll` / cwd-relative path | N/A (no I/O) |

## Verified

- TDD: with the `settingNoticeKey` change reverted, the new
  `TestPluginSettingsPage_GET_AIImageEndpointInvalidHint` fails; restored, it passes.
- `gofmt`, `go build ./...`, `go test ./...`, `golangci-lint run ./internal/pages/...` (0 issues).
- Guards: i18n, help-drift, help-topics, compliance-claims, competitor-naming,
  core-neutral, mail-addresses, data-access, no-showmodal, page-http-error,
  plugin-settings-bump, readme-local-links.
- Packs: `validate.sh` and `check-key-drift.sh` against this branch's
  `en.json` pass for de/es/pt (3373/3373, 0 drift).

**Verdict:** safe to merge.

**Noted, not changed:** the existing `image_model_unsupported` copy says
background removal "stays off", but since ADR-0154 the till falls back to
its built-in removal (the help page already says so) — filed as ut-docs#4448.
