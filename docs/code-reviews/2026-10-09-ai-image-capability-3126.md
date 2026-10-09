# Review: AI image capability — rembg background removal (ut-docs#3126)

- **Date:** 2026-10-09
- **Card:** universaltill/ut-docs#3126 (card B of `architecture/ai-product-photo.md` §9)
- **Branches:** universal-till `feat/3126-ai-image-capability`;
  ut-plugin-integration-ai `feat/3126-image-settings` (1.3.0)
- **Author model:** Opus 5.5 (Dev subagent). **Reviewer:** Fable (independent subagent).

## What shipped

- `internal/ai`: a `cutter` capability next to text AI. `Config.Image`
  `{Provider, Endpoint, Model}`, `Service.cut`, `CanCutout()` (nil-safe,
  independent of `Enabled()`), `RemoveBackground`. `FromEnv` reads
  `UT_AI_IMAGE_PROVIDER|_ENDPOINT|_MODEL`.
- Fail-safe, per ADR-0126 §7: the adapter is built only when the provider is
  exactly `self_hosted`, the endpoint passes `plugins.ValidEndpointURL`, and
  the model is on the licence-checked allow-list (`birefnet-general-lite`
  (the default, MIT) or `u2netp` (Apache-2.0)). Anything else is off, and no
  HTTP call is made. That includes rembg's own default `bria-rmbg`, which
  needs a paid commercial licence.
- `internal/ai/rembg.go`: sends `POST <endpoint>/api/remove` as multipart
  with fields `file` and `model`. These field names were checked against
  rembg's `s_command.py`.
  - Timeout is 90 s; the answer is capped at 20 MB and an oversize answer is
    an error, never a truncation.
  - The answer must carry the PNG signature, is decoded with
    `imaging.DecodeBoundedFormats` (bounded by `MaxPixels`), and must contain
    at least one non-opaque pixel.
  - The client comes from `netaccess.NewClient` and follows no redirects: a
    3xx answer is an error.
- `internal/pages/ai_resolve.go`: plugin `image_*` settings → env → off.
  - Text resolution is unchanged.
  - When the plugin explicitly names a provider that isn't `self_hosted`, or
    an invalid endpoint, the result is off, without falling through to the
    env image config.
- Plugin settings page: a plain (non-modal) hint above `image_model` when the
  stored model is off the allow-list. New key
  `plugins.settings.ai.image_model_unsupported` in en/ar/fa/tr.
- Manual: `web/help/{en,de,ar,fa,tr}/plugins.md` describe the image settings
  and the hint.
- README: the env vars and settings.
- ut-plugin-integration-ai 1.3.0: three new settings —
  - `image_provider`, default `self_hosted`;
  - `image_endpoint`, `type: "endpoint"`, default `""`, so the capability is
    off until a shop sets it;
  - `image_model`, default `birefnet-general-lite`.

  The README covers the rembg setup and the licence warning.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The help topic didn't mention the image settings or the new hint (#324). | **Fixed:** one sentence in all five help locales. Topic hashes refreshed with `make docs-shots`. The PNGs were byte-different only because of the container's renderer, so they were restored. Guard green. |
| 2 | should-fix | The `mediaType` passed by the caller was copied verbatim into a MIME part header, so CR/LF could inject parts (for example a different `model`) once card C forwards the upload's declared type. | **Fixed:** only `image/png` and `image/jpeg` pass; anything else becomes `application/octet-stream`. `TestRembgFormSanitisesMediaType` failed before the fix and passes after. |
| 3 | should-fix | The new en.json key is missing from the de/es/pt language packs. `lang-pack-drift` blocks on `main`. | **Done in the same cycle:** pack PRs merged after the core change (reviewer step 4). |
| 4 | nit | The hint names the allowed models literally, so a new model means editing 7 locale files. | Accepted: the allow-list grows only after a manual licence check, so a copy edit at that point is fine. |
| 5 | nit | No hint when an unknown `image_provider` is set. | Accepted: design §4 asks only for the model hint. The capability stays off, which is correct. |
| 6 | nit | Adapter errors include the endpoint URL. | Accepted: nothing logs them. Card C must not show `err.Error()` in the browser; this is noted on #3127. |
| 7 | nit | `UT_AI_IMAGE_MODEL` without an endpoint left a non-zero `Config.Image`. | **Fixed:** with no endpoint, the image config is the zero value. |
| 8 | nit | The env provider is lower-cased, but the plugin value must match exactly. | Accepted: this mirrors the text side's `FromEnv`. |
| 9 | nit | A garbled comment in `settingNoticeKey`. | **Fixed.** |
| 10 | nit | Plugin 1.3.0 describes a feature whose till UI is card C. | Accepted: until card C ships, the settings are inert and default to off. |

## Verified beyond the unit tests

- The reviewer re-verified five TDD claims by mutation in a separate
  worktree, and each test failed as expected:
  - removing the redirect refusal;
  - letting an unknown provider build the adapter;
  - bypassing the allow-list;
  - removing the size cap;
  - letting a plugin with an unknown provider fall through to the env config.
- I re-verified the new media-type test: it fails without the fix
  (`text/html` and the CRLF value kept as the header) and passes with it.
- `resolveAIConfig` precedence checked by hand for all four (textOK, imageOK)
  combinations: the text result is identical to the pre-change code.
- `go test -race ./internal/ai/...` is clean.
- No visual surface was driven in a browser: the only UI change is one
  `<p class="muted">` line, covered by a handler test. It renders only when a
  shop has saved an off-list model.

## Gates

- `gofmt`, `go build ./...` and `go vet` pass.
- `golangci-lint`: 0 issues.
- `go test ./internal/ai/... ./internal/pages/...` passes.
- `internal/plugins` passed in Dev with `-timeout 25m`.
- Guards pass: i18n, core-neutral, netaccess, help-topics, help-drift,
  compliance-claims, competitor-naming, docs-shots, plugin-settings-bump and
  no-showmodal.
- These guards couldn't run in the cloud container: shellcheck-version,
  deadcode-baseline and gobind-skip. None of them relate to this diff.

## Verdict

Safe to merge.

## Deferred

- Card C (#3127): the endpoint and picker UI.
- Card D (#3128): homelab rembg and the device check.
- `min_pos_version` stays at 1.0.0. Tills older than v0.30.16 can't parse
  `type: "endpoint"`; this is the same exposure every endpoint-typed plugin
  already has.
