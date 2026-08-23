# Code review: ut-docs#628 follow-ups (plugin chip tokens, deterministic order, ADR note)

**Date:** 2026-08-23
**Branch:** `fix/628-plugin-review-followups`
**Card:** ut-docs#628

## What shipped

Four small, unrelated, non-blocking follow-ups noted during the ut-docs#368
review, batched into one card because none change behavior:

1. **`web/ui/pages/plugins.html`** — the enabled/disabled/broken/update-
   available status chips hardcoded one-off inline colors instead of
   reusing the `--success`/`--danger`/`--warning`/`--muted` design tokens
   sibling pages (`shifts.html`, `inventory.html`) already use for the
   identical semantics. Migrated all four to the established token
   patterns; the disabled chip needed no override at all — `.tag`'s own
   default styling already is that neutral look.
2. **`internal/data/plugin_repo.go`** — `PluginRepo.ListInstalledPlugins`
   had no `ORDER BY`, so which plugin `WasmRuntime.Sync` iterates last was
   dependent on SQLite's unspecified row order. Added `ORDER BY id`
   (`plugins.id` is `TEXT PRIMARY KEY`, so this is a well-defined
   lexicographic order).
3. **`ut-docs/adr/0011-multi-till-sync.md`** — the 2026-08-13 amendment
   documenting the ut-docs#368 tax fail-closed behavior didn't call out
   the one already-implemented exception: a DB-read failure in the
   fail-closed check itself (`pluginTaxRateAsker.taxAuthorityBroken`)
   fails *open*, not closed, and is logged rather than silent. Added one
   sentence noting it.
4. The fourth original item (a duplicated `"broken"` string literal
   between `sync_admin.go` and `wasm_runtime.go`) turned out to already be
   fixed by an unrelated later commit — `data.PluginStateBroken` is now
   the single shared constant both call sites use. No action needed;
   confirmed by grep before starting and independently re-confirmed by
   the reviewer.

## Independent review

Spawned a fresh-context Sonnet subagent (card is `complexity:easy`, per
the scrum-master skill's model-routing table) with the full diff and
explicit instructions to actually run the build/tests/guards rather than
just read the diff. **Verdict: SAFE TO MERGE**, no blocking issues.

Verified independently by the reviewer (not just asserted):
- The rgba-tint values (`rgba(22,163,74,.12)` / `rgba(220,38,38,.12)`)
  match `shifts.html`/`inventory.html`'s existing pattern exactly.
- `--success`/`--danger`/`--warning`/`--muted` all resolve as claimed in
  `web/public/app.css`'s `:root` block; `.tag.warn` is a pre-existing
  class already used elsewhere, not newly invented.
- `plugins.id` is `TEXT PRIMARY KEY`; all four real callers of
  `ListInstalledPlugins` (`WasmRuntime.Sync`, `Manager.loadInstalled`,
  `UpdateChecker.getInstalledPlugins`, `TelemetryClient.ReportNow`) build
  a map or otherwise don't depend on the old unordered iteration order.
- `taxAuthorityBroken` in `internal/pages/tax_hook.go` really does fail
  open (not blocked) on a DB read error and logs via
  `logging.L().Errorf(...)` — matches the ADR sentence word for word,
  and is covered by the existing `TestTaxAuthorityBroken_DBErrorFailsOpenButLogs`.
- No new i18n keys, no hardcoded user-facing strings, no client/shop
  names, nothing secret-shaped, no scope creep beyond what #628 describes.

Nitpicks raised, both accepted as-is (deliberate, non-blocking):
- ADR-0011's top-of-file "amended by" status header wasn't bumped for
  this addition — reasonable, since this documents an existing
  already-implemented exception rather than a new decision.
- The template comment explaining the token migration is a bit long for
  a one-off style change — a style opinion, not a defect.

## Verified beyond automated tests

- `gofmt -l .` — clean.
- `go vet ./...` — clean.
- `go build ./...` — clean.
- `go test ./internal/data/... ./internal/plugins/... ./internal/pages/...`
  (all packages `ok`, run twice — once by this session, once
  independently by the reviewer subagent).
- CI guards run locally: `guard-i18n.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-compliance-claims.sh`,
  `guard-data-access.sh` — all pass.
- `make docs-shots` regenerated (the chip color change is a real pixel
  change on the `/plugins` help topic's screenshotted page); the guard's
  aggregate `web/ui/**`+`web/public/**` surface hash means unrelated
  topics' screenshots (`alerts`, `designer`, `invoices`, `sell`) also
  regenerated — expected behavior of the guard's own aggregate-hash
  design (confirmed by reading `guard-docs-shots.sh`'s header comment),
  not an unintended scope leak.
- `web/help/en/plugins.md`'s prose doesn't assert any specific chip
  colors, so no manual text went stale.

**Known local-environment limitation, not a finding against this diff:**
`go test -race` on `internal/data`, `internal/plugins`, and
`internal/pages` times out (10 min per-package default) in this sandbox
— reproduced identically by checking out unpatched `origin/main` into a
separate worktree and running the exact same command, so it's a
pre-existing `-race` + pure-Go SQLite migration slowness in this
constrained environment, unrelated to this change. The non-`-race` run
passed cleanly; GitHub Actions CI (unconstrained runners) is the
authoritative `-race` gate here.

## Safe-to-merge verdict

Yes. Independent review found no blocking issues; all four sub-items
verified against the real code (not asserted); build/vet/tests/guards
all green.

## Explicitly deferred

Nothing — this card is fully closed by this change (the constant-sharing
item was already resolved before this card started).
