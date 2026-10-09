# Review — Plugins page Docs button follows the slot gate (ut-docs#3994)

Date: 2026-10-09 · Lane: `lane:cloud-54` · Complexity: easy
Author: Sonnet 5.5 (dev subagent) · Reviewer: Opus 5.5, fresh context

## What shipped

- `internal/pages/plugin_slot.go`: `pluginEntrySlotAllowed(d, r, slot)` —
  no slot = allowed; else `pluginSlotGates[slot]` must exist and pass (fail
  closed for `setup.wizard.steps` and unknown slots).
- `internal/pages/plugin_page.go`: `servePluginEntry` uses the helper in
  place of its inline check (identical behaviour, #3973).
- `internal/pages/plugins_page.go`: a `docs` entry the viewer's slot gate
  refuses gets no Docs button, so the button can never disagree with its
  route (the "shown, then refused" class #3982 fixed for `/menu` tiles).
- Tests: `TestPluginsPage_UngatedSlotDocsEntryHidesDocsRoute_3994`
  (`setup.wizard.steps`, unknown slot) and
  `TestPluginsPage_GatedSlotDocsEntryFollowsRoleGate_3994` (UT_AUTH on,
  real role lookups: `plugin_management` without `reports` → no button;
  with both / admin → button).
- `docs/plugin_guidelines.md` (+ ut-docs `reference/plugin-views.md`).

AC choice: the card offered refusing `slot` on `docs` at install, or gating
the button. Gating was chosen: it also covers plugins already installed with
such a manifest, needs no manifest-contract change, and reuses the route's
exact gate.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | info | Two docs entries for one plugin: a gated-out first entry lets the second (allowed) one supply the route. | Accepted — better than before; key is unique in practice. |
| 2 | nit (pre-existing) | Two entries sharing one route: `findPageEntry` dispatches the first. | Out of scope; existing route-collision behaviour. |

No blocking findings. Recurring bugs (`os.MkdirAll`, cwd-relative paths):
N/A, no file I/O.

## Verification

- TDD re-verified by the reviewer in a separate worktree: with the old
  `plugins_page.go` all three negative cases fail
  (`docsRoute = "/plugin/slotdocs/docs", want empty …`); restored, all pass.
- `gofmt -l .` clean; `go build ./...`; `go vet ./internal/pages/`;
  full `go test ./internal/pages/` ok (≈217 s); guards card-data-schema,
  competitor-naming, compliance-claims, core-neutral, data-access,
  help-drift, help-topics, i18n, kiosk-engine, no-showmodal,
  no-inline-handlers, page-http-error, plugin-menu-read, netaccess,
  pipefail-grep-q, readme-local-links, store-private pass locally.
- Not run locally: `guard-deadcode-baseline.sh` and `golangci-lint` — the
  container's tool builds are older than the repo's Go 1.27 target; CI runs
  both.
- UX: no new string or layout. The only visible effect is the existing
  "no Docs button" state (already covered by
  `TestPluginsPage_NoDocsEntryMeansEmptyDocsRoute`); no screenshot taken
  since no rendered surface changed shape. The `web/help/en/plugins.md`
  wording ("a plugin that ships its own documentation shows a Docs button")
  stays accurate.

## Verdict

Safe to merge.
