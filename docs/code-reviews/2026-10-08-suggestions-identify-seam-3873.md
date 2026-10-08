# Review: `suggestions` component and the sell-screen `catalog.identify` seam (ut-docs#3873)

**Card:** ut-docs#3873 (ADR-0121 build 8c), complexity:hard.
**Author:** Opus 5.5 (lane:cloud-41, dev subagent). **Reviewer:** Fable (independent subagent, fresh context, in its own worktree).
**Branches:** universal-till `feat/3873-suggestions-identify-seam`; ut-docs `docs/3873-suggestions-identify` (`reference/plugin-views.md`).

## Scope split

The card asked for three things. This PR ships two of them:
- the `suggestions` component;
- the sell-screen `catalog.identify` seam.

Two parts moved to new cards, so this card does not ship half and call it done:
- **#3956:** the item-form "suggest details" action through `item.edit.actions` and `apply_fields`. It is blocked:dep on #3872, whose slot PR universal-till#1769 is still open.
- **#3957:** plugin-blob thumbnails on candidates. They need their own image-serving route.

The `apply_fields` allow-list itself is enforced already, which is the card's acceptance criterion.

## What shipped

### `internal/pluginview/suggestions.go`

The `suggestions` component. It holds 1–20 candidates. Each candidate has a label, an optional detail and exactly one effect:
- `add_to_basket {sku 1–128 bytes, qty 1–999}`, or
- `apply_fields`, limited to the item-form v1 allow-list: `name`, `sku`, `barcode`, `category`, and `price` as integer minor units ≥ 0.

Placement rules:
- `Context.Seam` gates where the component may appear.
- A plugin page or slot refuses it.
- `SeamSellIdentify` takes `add_to_basket` only; `SeamItemForm` takes `apply_fields` only.
- A seam document may hold only `text`, `notice` and `suggestions`.
- A redirect or job answer is refused in a seam.
- `thumbnail` is refused as an unknown field.

### `internal/pages/plugin_identify.go`

`POST /api/pos/identify/plugin` takes one sniffed JPEG/PNG/WebP photo of at most 8 MiB. It:
- stages the photo as an upload handle (#3793);
- runs `catalog.identify` as a job (#3908) for the lexically first subscriber that holds `events:receive`.

`GET …?_job=` polls the job. The result:
- a `/api/pos/scan` button per candidate, with the catalog item's photo resolved by `POSRepo.ResolveShortcutLine`;
- or "No match found";
- or "Identification failed".

The poll never extends the session (`auth.pluginJobPoll`). Demo mode denies both routes.

### Sell page (`index.html`, `app.js`, `app.css`)

- The plugin's camera-identify button replaces the built-in AI one, so there is never more than one.
- The overlay is a plain, non-modal div with the same camera handling as the AI overlay.
- A generation counter and `htmx:beforeSwap` drop late answers after Close or Retake.
- The video is hidden while results show, so they fit at 1024×600.

### Other changes

- **Locale keys:** none added. The seam reuses `ai.identify.*`, `plugin.job.*`, `plugin.view.unavailable` and `suggest.add`.
- **Help:** `web/help/{en,de,ar,fa,tr}/sell.md` each gain one paragraph. The German wording was checked against `ut-plugin-language-de`.
- **Developer doc:** `docs/plugin_guidelines.md` gains a `catalog.identify` section.
- **Docs screenshots:** the `docs-shots` manifest was regenerated.

## Findings (Fable review)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `identifyPluginID` used `plugins.CheckPermission` on every sell-page render and every 1 s poll. A subscriber without `events:receive` wrote one `permission_denied` audit row each time, about 60 a minute during an identify. | Fixed. The resolver now does a plain repo read. The poll takes the plugin from the job (`pluginJobRegistry.owner`) and never re-resolves. Tests: the no-audit assertion in `TestIdentifyPluginID_3873`, and `TestPluginIdentify_PollUsesTheJobsPlugin_3873`. Both were seen failing first. |
| 2 | should-fix | Retake or Close while the POST was in flight left an unpolled job holding the slot for 15 s. On a tablet (one job per plugin) the next Capture got 429. | Fixed. When a capture hits busy, the seam's own running jobs are cancelled (`cancelRunning(plugin, identifyRoute)`) and the start is retried once. A plugin-page job is never cancelled and still answers 429. Tests: `TestPluginIdentify_NewCaptureReplacesEarlierJob_3873` (seen failing with 429 first) and `TestPluginIdentify_Busy429_3873`, which now occupies the cap with a page job. |
| 3 | nit | A tapped suggestion closed the overlay even when the scan request failed. | Fixed: it closes only when `ev.detail.successful` is true. |
| 4 | nit | The filename was always `capture.jpg`, even for a PNG or WebP. | Fixed: the filename follows the sniffed type. |
| 5 | nit | The 204 branch checks `HX-Request` but not `IsFragmentSwap`. | Accepted. The identify answers are always fragments, never a full page. |
| 6 | nit | The overlay painted any non-OK body. | Fixed: only the seam's own statuses (200/400/404/413/429/502/503) are painted; anything else shows the error status. |
| 7 | nit | `ItemFormFields()` has no production caller. | First accepted (for #3956), then CI's `guard-deadcode-baseline` refused it: removed. The allow-list stays private (`itemFormFields`); #3956 exports what it needs when it has a caller. |
| 8 | observation | `/api/pos/*` POSTs have no Origin or CSRF check. | Not introduced here; the new POST matches `/api/pos/identify` and `/api/pos/scan`. Cookie is SameSite=Lax. |

## TDD re-verification

- **Tester:** mutation checks found one false pass. The allow-list refusal test passed with the allow-list removed, because `"cost":1` was refused by the type check instead. Fixed with string-valued off-list cases.
- **Reviewer**, in its own worktree: reverted the allow-list check and the auth poll exemption, saw `TestSuggestions_Refusals_3873` and `TestIdentifyJobPollNeverExtendsTheSession` fail with the expected errors, restored, and saw both pass.
- **Review fixes:** the three new tests were run against the pre-fix code and failed:
  - `TestIdentifyPluginID_3873`: "resolver wrote 1 permission_denied audit rows, want 0";
  - `TestPluginIdentify_PollUsesTheJobsPlugin_3873`: "poll re-resolved the identify plugin";
  - `TestPluginIdentify_NewCaptureReplacesEarlierJob_3873`: "identify start = 429".

  All pass after the fix.

## Verified beyond automated tests

Tester drove the real binary, templates and `app.js` in Chromium with a fake camera. The `catalog.identify` subscriber was a temporary build-tagged Go handler, since removed:
- **Checks:** 54 at 1024×600 and 360×740, plus fa (RTL) and dark at both sizes.
- **Flows:** capture → progress → matches → tap adds the line through `/api/pos/scan` and closes the overlay.
- **Close before the result:** no later polls or swaps, and scanning still works.
- **Retake** drops the job.
- **Unknown SKU:** the normal "not found" alert.
- **Modal and touch:** no `showModal`; touch targets at least the base button height; no horizontal scroll.

The screenshots were looked at by Tester and by the orchestrator (en 1024 and fa 360 results). Tester found and fixed two layout bugs:
- the detail-line contrast;
- the controls falling below the fold at 1024×600.

The review fixes in `app.js` (nits 3 and 6) were not re-driven in a browser. They are small conditionals, and `node` parses the file.

## Not verified

- A real WASM plugin; a Go-handler stand-in was used on the real bus and job path.
- A real camera and real touch hardware.
- The plugin overlay's camera-error branches (copied from the AI overlay).
- Sell→Menu→Sell camera release for the plugin overlay.

## Gate

- `gofmt -l .`: empty. `go build ./...` and `go vet ./...`: clean. The full `go test ./...` result is recorded in the close-out comment.
- Every guard in `ci.yml`'s build job passes, except two that can't run in this container: `guard-shellcheck-version` (no shellcheck; no `.sh` changed) and `guard-deadcode-baseline` / `golangci-lint` (toolchain older than go1.27).

## Deferred

- #3956: item-form `apply_fields`.
- #3957: blob thumbnails.
- A pre-existing nit, shared with the AI overlay's CSS: at 1024×600 a full-height panel ends about 3 px under the status bar. Filed as ut-docs#3960.

**Verdict:** safe to merge once CI is green.
