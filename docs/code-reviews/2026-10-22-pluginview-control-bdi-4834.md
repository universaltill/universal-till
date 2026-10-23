# Review — plugin view controls and job text run in their own direction (ut-docs#4834)

**Date:** 2026-10-22 · **Built by:** Opus 5.5 · **Independent review:** Opus 5.5 subagent (fresh context, no authoring context).

## What shipped
- `web/ui/partials/pluginview/controls.html`: `<bdi>` around the plugin's form field labels (every kind, the toggle included), the form's submit label and action/empty-state button labels. A `<label>` wraps its input, so the text is isolated inside it rather than through `dir` on the label. `<option>` holds text only, so it carries `dir="auto"`. The chat thinking bubble's `.Poll.Message` is a `<bdi>`.
- `web/ui/partials/pluginview/poll.html`: the job's `.Job.Message` is a `<bdi>`. Core's own fallback text gets no `<bdi>`.
- `.Paging.Text` is unchanged. It is core's own text, built only from `plugin.view.page_n(_of)` and localized numbers (`internal/pluginview/render.go`), so no plugin text reaches it.
- New test `internal/pages/pluginview_dir_4834_test.go` renders every control literal in `fa`, plus a job poll and a chat thinking bubble. For each literal, the element whose whole text it is must be a `<bdi>` or carry ` dir="auto"`. A poll showing core's fallback text must have no `<bdi>`.
- `plugin_view_test.go`: two assertions now expect `<option value="b" selected dir="auto">`.
- `web/help/img/manifest.json`: only the surface hash changed (`update-docs-shots-surface-hash.sh`). No docs-shot renders a plugin view, and a `<bdi>` around English text on an LTR page renders identically.

## Findings
| # | Sev | Finding | Handling |
|---|---|---|---|
| 1 | High | Three existing tests asserted `<option value="b" selected>` | Fixed. The reviewer suggested putting `dir="auto"` after `selected`, but that still breaks the `selected>` substring, so the assertions were updated instead. |
| 2 | Medium | Untracked `zz_*_test.go` files and a hook call in the new test | These were the author's temporary screenshot dump, already removed before commit. |
| 3 | Low | The `dir="auto"` check was a bare substring, so it would also accept `data-dir="auto"` | Fixed: the check now matches ` dir="auto"` |

The reviewer confirmed the following:
- The HTML is valid.
- No CSS or JS selector depends on bare label or button text.
- Labels already lay out as a column.
- The test is real: with the old partials it fails for every literal.

## Verified beyond automated tests
The rendered fragment was screenshotted in Chromium at 1024×600, in a `lang="fa" dir="rtl"` page with `web/public/app.css`. Every Latin label, option, button, job message and thinking-bubble text has its full stop at its own end. Labels, fields, buttons and the progress bar still follow the RTL page.

## Gate
- Passed: `gofmt`, `go vet` and `golangci-lint` (0 issues) on `internal/pages`.
- Passed: `go test ./internal/pages/`.
- Passed guards: i18n, rtl-logical-css, no-inline-handlers, no-showmodal, compliance-claims, competitor-naming, mail-addresses, help-topics, core-neutral, data-access, kiosk-engine, autofill-suppression and docs-shots.
- Not run in this container: the full `go test ./...`. CI runs it. Only templates and `internal/pages` tests changed.

## Safe to merge
Yes.
