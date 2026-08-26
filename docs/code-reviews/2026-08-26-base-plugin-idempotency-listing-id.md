# Review: base-plugin install idempotency keyed on the wrong id (ut-docs#1063)

**Branch:** `fix/1063-base-plugin-idempotency-listing-id` · **Repo:** `universal-till`
**Dev model:** Sonnet (session) · **Review model:** Opus (independent subagent, isolated worktree)

## What shipped

`resolveAndInstallBasePlugin` (`internal/pages/setup_base_plugins.go`) auto-installs a
country's free base plugin (e.g. the German language pack) from the marketplace
catalog, and is meant to short-circuit when an equivalent plugin is already active —
avoiding a redundant install on a second wizard run, a background retry tick, or a
second click of the setup-wizard language tile.

The idempotency check called `data.NewPluginRepo(d.Db).PluginActive(ctx, pluginID)`
with `pluginID := best.ID` (falling back to `best.ListingID`) — the marketplace
**listing** id. `PluginActive` looks up the `plugins` table by its primary key, which
is the **manifest** plugin id (e.g. `ut-plugin-language-de`), assigned to the
plugin's own manifest and verified via Ed25519 at install time. Against the real
ut-cloud wire, `PluginSummary` has no separate manifest-plugin-id field at all —
`ID` and `ListingID` both decode to the same listing UUID (confirmed by the fixture
in `internal/plugins/marketplace/testdata/cloud_list_plugins_response.json`, which
has no `listing_id` key, and by `catalog_contract_crossrepo_test.go`). So the check
was querying `plugins.id = <listing-uuid>`, which can never match a row keyed by the
real manifest plugin id — **always false in production**.

## Fix

Replaced the check with a lookup against `plugins.NewInstallStatusStore(d.Db).Get(ctx,
listingID)` — a store already keyed by **listing id** (the identity actually known
before install), and the same identity model `cloudInstallPluginVersion` already uses
internally for its own upgrade/idempotency bookkeeping (`priorInstalled`). The check
now reads `status.State == plugins.InstallStateActive && status.PluginID != ""`.

Also made `TestResolveAndInstallBasePlugin_IdempotentWhenAlreadyActive` wire-faithful:
it now sets `ID == ListingID` on the fake catalog entry (matching what a real
marketplace always sends), instead of the old `deLanguageCatalogEntry` helper shape,
which modeled them as two *different* values — an unfaithful shape that let the old,
buggy check pass this exact test.

## Independent review (Opus, isolated worktree)

**Verdict: safe to merge, no blockers.** Full report on file; summarized here.

- Independently re-confirmed the bug premise from the raw wire fixture (not just
  trusting the issue text).
- Ran `gofmt`/`go build`/`go vet`/the full suite (41 packages, zero failures) and the
  CI-blocking guards this diff touches (`guard-data-access.sh`, `guard-docs-shots.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh`) — all green.
- **Independently re-verified the TDD claim**: hand-reverted only the production fix,
  confirmed the new wire-faithful test fails (`expected exactly one download-token
  request across both attempts, got 2`), then — going further than asked — also
  restored the *old* (pre-fix) test fixture against the same reverted code and proved
  the old test was a **genuine false pass** (passed only because its fixture set
  `ID != ListingID`, a shape no real server produces). Restored both files, all 7
  `TestResolveAndInstallBasePlugin_*` tests pass again.
- Checked the two recurring bug classes: no file I/O in the diff (`os.MkdirAll` N/A),
  no path construction in the diff (`paths.Data(...)` N/A).
- Verified the docs-shots regen mechanically (byte-diffed the two touched PNGs: ~20
  bytes differ out of 1.84MB each, max channel delta 2/255 — anti-aliasing jitter, not
  content drift; the guard's independently recomputed manifest hash matches what's
  committed).
- Confirmed no secrets/PII/real client names, and that the UX-guidelines checklist and
  help-topic-manual-update requirement genuinely don't apply (no route registered, no
  `web/**` authored file touched, nothing a shop owner sees or does changes).

### Findings

- **N1 (nit, applied):** the new check tested only `State == Active`, unlike its two
  sibling checks elsewhere in the codebase (`cloudInstallPluginVersion`'s
  `priorInstalled`, `sync_admin.go`'s `convergePluginSet`), which also require
  `PluginID != ""`. Not currently exploitable (every writer of an Active record sets
  `PluginID`), but costs nothing to match — **applied**, comment added explaining why.
- **N2 (non-blocker, recorded as a decision):** the fix is a genuine narrowing of
  intent — old code's comment claimed "any active plugin with this manifest id"; the
  new check is scoped to "this exact listing id". These differ only if the same
  manifest plugin is republished under a *different* listing UUID (a relist, or two
  listings serving the same pack during a staged rollout) — the fix would install
  again rather than recognizing the manifest as already active. Accepted deliberately:
  the old check's broader coverage never existed in production (it was always false),
  so this doesn't regress anything actually shipping; listing id is the only identity
  available before install and is what the rest of the installer stack already keys
  on (`cloudsync_wire.go`, `plugin_api.go`, `plugins_store_page.go`); and a merchant
  who explicitly disabled a plugin (`SetPluginActive(false)`, which doesn't touch
  `plugin_install_status`) now correctly short-circuits instead of being silently
  re-installed and re-activated — a net improvement in that direction.
- **N3 (housekeeping):** commit message and this review record — done as part of this
  close-out.

### Corrections to the issue's framing (worth recording)

- The issue's stated blast radius ("every 5-minute retry re-invokes the install") was
  overstated: `basePluginRetryTick` and `installBasePluginsForSetup` both drop a spec
  from the pending list the moment `resolveAndInstallBasePlugin` returns nil, so a
  successful install is never retried again by that path regardless of this bug. The
  real exposure was narrower: a second `POST /api/setup` for the same country, a
  `savePendingBasePlugins` persistence failure, and the caller below.
- A caller the issue didn't mention makes the fix more valuable than the retry framing
  suggested: `internal/pages/setup_language_catalog.go`'s `POST /api/setup/language`
  handler calls `resolveAndInstallBasePlugin` directly from a user click, bypassing the
  pending list — a second click in the window before `ReloadPlugins` completes could
  re-download the whole bundle in the foreground while the merchant waits. This fix
  closes that window too.

## Verified beyond automated tests

- Full `go test ./...` (no `-race`, per this repo's documented gate) — 41 packages,
  zero failures, run twice (once pre-N1-nit, once after).
- `-race` was attempted for the full suite but hit the pre-existing, already-tracked
  `internal/plugins`/`internal/pages` ~600s default-timeout flake on totally unrelated
  packages (ut-docs#1034, #1119, #776, #753, #878 all document this same pattern on
  clean `main`) — not caused by this diff; not required by this repo's committing gate.
- All 16 CI-blocking guards from `universal-till/CLAUDE.md`'s "Before committing" list
  run locally, all green.
- `make docs-shots` regenerated (this diff touches a non-test `.go` file under
  `internal/pages/**`, which `guard-docs-shots.sh` treats as a surface change even
  though `setup_base_plugins.go` registers no routes) — byte-diff confirms the two
  touched PNGs changed only by anti-aliasing jitter.

## Safe to merge

Yes. No blockers found by either the author or the independent reviewer.

## Explicitly deferred

- Nothing new deferred by this fix. (The two items already deferred by the parent
  #1055 review — on-screen annotation input, `web/help/{ar,fa,tr}` translations — are
  unrelated to this card.)
