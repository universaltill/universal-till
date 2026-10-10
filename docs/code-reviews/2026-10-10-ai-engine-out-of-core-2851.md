# Review — AI text engine out of core (ut-docs#2851)

Date: 2026-10-10 · Lane: lane:sdk · Author model: Opus 5.5 (dev subagent) · Reviewer model: Fable (independent)

## What shipped
- `internal/ai` deleted: the Ollama/OpenAI/Claude clients, the identify prompt and the Ask tool loop, with their tests. The engine now runs in `ut-plugin-integration-ai` ≥ 2.0.0 (WASM, ut-docs#4032). 2.1.0 is live in the marketplace snapshot.
- Background removal stays host-side (ut-docs#3126), moved to the new neutral package `internal/bgremove`. `rembg.go` is byte-identical apart from the package line. The resolution order (plugin image settings → `UT_AI_IMAGE_*` → off), the exact-`self_hosted` fail-safe and the model allow-list are unchanged.
- Deleted pages: `ai_api.go` (`POST /api/pos/identify` + confirm, `loadReferenceImages`), `ask_api.go` (`POST /api/reports/ask`), the built-in identify button/overlay, the Reports Ask card, `ask_answer.html`, and 8 unused locale keys (en/ar/fa/tr). `ai_resolve.go` is image-only. The text `UT_AI_PROVIDER|ENDPOINT|MODEL|API_KEY` env override is gone.
- Plugin seam untouched: `plugin_identify.go`, `identify_slot.go`, pick/confirmed flow, `ai_identify_confirmed` audit, the core read views. `ask_views_parity_test.go` became `core_views_parity_test.go`, which checks each view against the reports call it wraps.
- `go.mod`: `anthropic-sdk-go` dropped (`go mod tidy`).
- Guard: `guard-core-neutral.sh` fails if `internal/ai` exists or is imported (subpackages too). 3 new fixture cases.
- Help: `sell.md` (5 languages) and `en/reports.md` say identify/Ask come from the AI Assistant plugin 2.0+, and an older plugin must be updated. README, env examples and docs updated. docs-shots regenerated.

## Findings (Fable)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker (process) | Only deletions were staged; committing the index alone would not build | Fixed: everything staged before commit |
| 2 | should-fix | `anthropic-sdk-go` still in `go.mod` with no importer | Fixed: `go mod tidy` |
| 3 | nit | `docs/sale-screen-notifications.md` named `#ai-identify-status` | Fixed → `#plugin-identify-status` |
| 4 | nit | ADR-0085/0126 and ut-docs architecture docs name `internal/ai` as the home | Fixed in the ut-docs docs PR for this card |
| 5 | nit | `ai_cutout.go`/`ai_resolve.go`/`Deps.AI` now hold background removal only; a rename would read better | Accepted: names kept (no behaviour value, extra churn) |
| 6 | nit | ar/de/fa/tr `reports.md` never had the Ask section (pre-existing, baseline-tracked ut-docs#331/#341) | Accepted, pre-existing |

The reviewer saw 3 setup-wizard tests in `internal/pages` fail once while the 380 s plugins package ran in parallel. They passed on a re-run and in isolation, and are unrelated to this diff (concurrency flake).

## Verified beyond automated tests
- TDD re-verified by the orchestrator: with `origin/main`'s guard script, the new fixture cases fail (3 cases). With the branch's script, all pass.
- Driven run: a fresh till (seed_demo) with the OLD text env set (`UT_AI_PROVIDER=ollama`, `UT_AI_ENDPOINT`, `UT_AI_MODEL`) and `UT_AI_IMAGE_ENDPOINT`. `/` has no identify button, only the barcode overlay. `/reports` has no Ask form. `POST /api/pos/identify` and `POST /api/reports/ask` return 404. The log names the image env override only. Screenshots of sell + reports looked at, at 1024×600 and 360×740, light theme, en: nothing overlapping or missing.
- e2e: camera-error-branching-1292, catalog-camera-viewfinder-1472, pages, htmx-senderror-1287, worker-server-isolation-2345: 29 passed.
- Gate (dev + reviewer): gofmt, build, vet, `go test ./...` (race on pages + bgremove), golangci-lint v2.14.0 0 issues, guard-core-neutral(+test), guard-i18n, guard-help-drift, guard-data-access, docs-shots guards. The remaining local guard failures are macOS-only (BSD sed, awk locale, shellcheck 0.11 vs 0.9).

## Not verified
- Identify + Ask **with the plugin installed** on a real till with a real model (capture → tap → line added → `ai_ref` stored). Handler tests with a fake plugin cover the seam. The end-to-end device run is a follow-up card (#4006's tester note).
- Not checked: dark theme, RTL, or German rendering of the two screens (only removals, no new strings).

## Follow-ups
- Language packs de/es/pt carry the 8 removed keys as orphans: pack PRs in this cycle.
- e2e coverage of camera errors on the plugin identify overlay (needs a fake `catalog.identify` plugin in e2e): Backlog card.

**Verdict:** safe to merge.
