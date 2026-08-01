# Code review: export/report canonical-type plugin dispatcher

**Date:** 2026-08-01
**Scope:** `internal/plugins/ipc.go` (+`ipc_test.go`), `internal/data/plugin_repo.go`
(+`plugin_repo_test.go`), `internal/pages/data_api.go`, `internal/pages/settings_page.go`,
`web/ui/pages/settings.html`, `web/locales/{en,ar,fa,tr}.json`,
`internal/plugins/export_wasm_dispatch_test.go` + `internal/plugins/testdata/export_guest/main.go`.
Companion change: `universaltill/ut-plugin-tax-de` (`src/main.go`, `manifest.json`, `README.md`).
Doc fix: `universaltill/ut-docs` `reference/plugin-manifest.md`.
**Trigger:** ut-docs#189 — the plugin event bus already dispatches any
manifest-declared hook event generically (proven by the pre-existing
`tax.rate.ask` hook), but nothing in the till published an event for
`export`/`report`-type plugins, and `ut-plugin-tax-de`'s manifest never
declared its own placeholder event in `hooks[]` — so even a correct
dispatch would never have reached it.

## What shipped

- `PluginRepo.ListExportEntries` — active `export`/`report`-type entries
  from active plugins.
- Manager-gated `POST /api/data/export` (`from`/`to`/optional `entry_key`)
  resolves an installed entry, asks its owning plugin via the new
  `EventBus.AskPlugin` with `export.requested.ask`, and either streams the
  plugin's returned file (`Content-Disposition: attachment`) or shows its
  status message.
- A Data-settings-page UI trigger, all new strings localized in all 4
  locales.
- A real-wazero regression test (`TestExportDispatch_RealWasmModule`)
  proving dispatch reaches an actual compiled WASM plugin, not just an
  in-process fake handler — mirrors `wasm_hostfns_test.go`'s
  `buildHostfnGuest` pattern.
- Companion `ut-plugin-tax-de` change: renames its placeholder event to
  `export.requested.ask`, declares it in `manifest.json`'s `hooks[]` (it
  wasn't declared at all before — `EventBus.subscribe` requires an active
  declared hook, so this plugin could never have received any event on
  this path regardless of host-side wiring), and answers with a real
  `{ok,message}`/`{ok,error}` JSON response instead of silently
  `os.Exit(0)`-ing with nothing on stdout.
- `ut-docs/reference/plugin-manifest.md` — replaces a stale note that
  conflated "engine renders page/button/theme natively" (UI-surface
  rendering — still true) with "no dispatcher exists for other types"
  (false — event/hook dispatch has been generic since `tax.rate.ask`).

## Independent review (different-model subagent, Opus, genuinely adversarial)

Ran the real build/vet/test/guard gate itself (not just read the diff),
wrote and ran six throwaway probe tests against the live handler, and
checked cross-repo contract drift, CRLF/header-injection exploitability,
locking/offline-first interaction, and Ed25519-verification bypass risk —
all independently, not taken on trust. Full findings and verification
detail are in the review transcript; summarized here.

**BLOCKING (fixed):** the handler resolved `entry_key` to a specific
owning plugin, then asked via a **broadcast** `EventBus.Ask` — which
returns the first *any* subscriber's non-empty answer, not necessarily the
resolved plugin's. Probed live: with two installed export plugins, a
request for one plugin's entry got served by the *other* plugin's answer;
an entry whose own plugin had no `export.requested.ask` hook at all still
got served by an unrelated plugin. The companion `ut-plugin-tax-de` change
made this worse, not better — it answered unconditionally regardless of
`entry_key`. On a till running two export plugins, this meant "the
merchant clicks CSV Export and gets a DSFinV-K trigger instead."

**Fix:** added `EventBus.AskPlugin(ctx, pluginID, event, payload)` —
`Ask`'s exact semantics restricted to one already-identified plugin — and
routed `/api/data/export` through it via `entry.PluginID`. `ut-plugin-tax-de`
now also declines (doesn't answer) if `entry_key` doesn't match its own
entry, as defense-in-depth for a future second export entry in that same
plugin — the host-side fix is what actually closes the cross-plugin leak.
Verified as a genuine regression, not just a diff read: wrote
`TestEventBus_AskPlugin_DoesNotAcceptAnswerFromOtherSubscriber` (unit,
`internal/plugins`) and `TestExportDispatch_NeverAnswersFromWrongPlugin`
(HTTP-level, `internal/pages`), confirmed both **fail** against the
reverted (`Ask`-based) code with the exact leaked-wrong-answer symptom the
reviewer found, then confirmed both pass restored.

**SHOULD-FIX (fixed):**
- Plugin-supplied `filename` went into a raw quoted `Content-Disposition`
  header — a `"` in a crafted filename could inject `filename*=` (RFC 6266
  takes precedence) and rename the downloaded file. CRLF/header injection
  itself was checked and is **not** exploitable (`net/http` sanitizes
  control characters), so this needed the fix, not just the check. Now
  built with `mime.FormatMediaType`, matching the existing safe idiom
  already used elsewhere in this repo (`plugin_api.go`).
- Client-side file-vs-JSON detection used `Content-Type` substring
  matching, which misreads a legitimate JSON-formatted export file (e.g.
  DATEV/ELSTER) as the status envelope and silently never downloads it.
  Switched to the actually-reliable signal: `Content-Disposition` presence
  (only ever set on the file branch).
- No `from <= to` validation — accepted and dispatched an inverted range.
  Added.

**Also addressed in this pass (should-fix/nit, cheap alongside the above):**
- "multiple export **plugins** installed" was wrong for one plugin
  shipping two export entries — reworded to "entries".
- Empty `message`/`error` from a malformed plugin response rendered as a
  bare `✓ `/`✗ ` in the UI — added fallback text.
- `settings_page.go` silently swallowed `ListExportEntries`'s error,
  making a real DB failure indistinguishable from "no plugin installed" —
  now logged.
- Added test coverage the reviewer flagged as missing for the new
  validation paths: `TestExportDispatch_FromAfterTo`,
  `TestExportDispatch_InvalidBase64Content`, plus the two regression
  tests above.

**Deferred to Backlog (not blocking merge — tracked as new cards, not
silently dropped):**
- Truncate `.ask` event answers before they hit the wasm result INFO log
  (`wasm_runtime.go` — pre-existing line, but this diff is what makes it
  load-bearing: an export answer can be a full sales dataset, not a small
  rate value).
- Per-event-class timeouts / streaming for large exports — today's 2s/10s
  timeouts and full-buffering were sized for `tax.rate.ask`-sized answers.
- Document the `export.requested.ask` payload/response contract in
  `ut-docs` with a cross-repo guard test, mirroring the existing
  manifest-signing contract test — today nothing but a manual read
  enforces the plugin/host shape staying in sync.
- Extend `guard-i18n.sh` to scan inline `<script>` string literals — a
  pre-existing blind spot across the whole repo, not introduced by this
  diff, but this diff's UI script does have a couple of small hardcoded
  English fallback strings that the guard doesn't catch.
- `README.md`'s Germany-fiscal-compliance claim ("zero code exists") is
  now stale beyond just this dispatcher — pre-existing, unrelated to this
  diff's scope, flagged for a separate pass.

## Verification (self, after applying the fixes above)

- `go build ./...`, `go vet ./internal/...`: clean.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`: green.
- `gofmt -l` on every touched Go file: clean.
- `go test ./internal/data/... ./internal/pages/... ./internal/plugins/... -count=1`:
  all pass, including the real-wazero `TestExportDispatch_RealWasmModule`
  (log-confirmed real module load + execution, not a stub).
- `go test ./... -count=1`: clean except the standing, pre-existing
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure` failure
  (root-in-container ignores read-only-dir permissions) — present
  identically on unmodified `main`, unrelated to this change, documented
  in prior review records in this same directory.
- `ut-plugin-tax-de`: `bash scripts/build.sh` (wasip1 build) and
  `bash scripts/validate.sh` (manifest validation) both green after the
  companion fix.

## Verdict

**Safe to merge** with the blocking fix applied and independently
re-verified (revert → fails with the exact reported symptom → restore →
passes). Should-fix items landed in the same pass; deferred items are
tracked as new Backlog cards, not silently dropped.
