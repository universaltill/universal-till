# 2026-08-13 — Join snapshot leaves a plugin registered but unloadable, silently mis-taxing sales

**Card:** universaltill/ut-docs#368
**Branch:** `pipeline/368-join-plugin-binary-gap`
**Model routing:** complexity:hard — built by a Fable dev subagent (three passes:
initial build, then two rounds of fixes), independently reviewed by Opus
(two full rounds, both worktree-isolated — a blocker-finding first round
earned a second round; that round found a second, still-open route to the
same class of bug, earning a third fix pass, which the orchestrator verified
personally with its own revert-then-restore proof rather than a fourth
subagent round).

## Requirement

A joined replica's `plugins` DB row can say "installed at version X" while
the actual wasm binary file is missing — the join snapshot copies DB rows,
never files. `WasmRuntime.Sync` used to silently `continue` on a load
failure, with no tally and no visible state change — and worse,
`pluginTaxRateAsker.AskTaxRateBP` then treated "no subscribers" as "no tax
plugin was ever installed" and silently fell back to the item's base tax
rate. Two tills in the same shop could compute and print **different tax**
for the same item, with the replica silently wrong and no signal to anyone.
p1/security.

Product owner's recorded decision (issue comments, 2026-08-13): fail
**closed**, scoped narrowly — block only the sales path the broken plugin
owns, never silently substitute a default rate, surface it loudly.

## Design

`universal-till` only — no new repo, no new ADR (extends the exact
mechanism ADR-0011 already documents; one amendment paragraph added there).
Four parts:

1. **Detect + surface**: `WasmRuntime.Sync` tallies load failures and flips
   the plugin's row to `install_state='broken'` (flipping back to
   `installed` on a later successful load) — the first real use of the
   `broken` enum literal that had existed in `001_init.sql`'s column
   comment since day one but was never actually written anywhere. A red
   "Broken ⚠" chip on the plugins page (`state` was already threaded into
   the template but never rendered — a pre-existing dead field, now used).
2. **Self-heal via the existing ut-docs#460 mechanism**: `convergePluginSet`
   (the replica's 30s pull-tick reconciliation, built for #460) now also
   treats "broken at the correct version" as drift needing re-fetch, not
   just a version mismatch — reusing the exact same Ed25519-verified
   marketplace re-fetch path, zero new transfer mechanism.
3. **Fail closed on tax**: a new repository query
   (`HasBrokenActivePluginForEvent`) answers "is an ACTIVE plugin
   manifest-registered for `tax.rate.ask` currently broken?" — independent
   of whether it's loaded right now, which is exactly why it still answers
   correctly while the plugin can't subscribe to the bus. `AskTaxRateBP`
   gained a third `blocked` return; both the cashier tender path and the
   self-order kiosk refuse to complete a sale on any line whose tax
   authority is blocked, with a translated message, rather than recording
   the base rate.
4. **Regression coverage**: a full two-till end-to-end test using a real
   compiled wasm guest and a fake marketplace, simulating exactly what a
   join snapshot produces (DB row, no file) through detection, fail-closed
   checkout, self-heal, and recovery.

## Independent review — round 1 (Opus, fresh context, worktree-isolated)

Found **1 BLOCKER, 3 MAJORs, several MINORs**:

- **BLOCKER**: `markBroken` demoted the plugin's `plugin_install_status`
  record to `Failed`. `convergePluginSet`'s prune loop only prunes a
  listing when the record's `State == Active` — so once broken, if the shop
  owner removed the plugin on the primary, the replica's prune loop
  permanently skipped it (replicas also reject manual uninstall). A broken
  plugin could never be removed from a replica again. Reproduced empirically
  by the reviewer with a throwaway probe test.
- **MAJOR**: the "remove the item, the rest stays sellable" tender message
  was false whenever the till's only tax authority was the broken plugin —
  every line blocks in that case (the asker has no way to know which
  specific lines a broken plugin would have answered for), so removing one
  item just moves the identical block to the next.
- **MAJOR**: file-imported tax plugins (no marketplace listing) can never
  self-heal via the sync loop, and the help text claimed automatic recovery
  unconditionally.
- **MAJOR**: the marketplace re-fetch on a broken plugin had no cap or
  backoff — a genuinely unfixable case (e.g. incompatible binary) would hit
  the marketplace every 30s forever.
- **MINORs**: a DB-error fail-open path with no log line; a long message
  string rendered inside a small pill on the store page; a chip indentation
  mismatch.

TDD claims independently re-verified by real revert-then-restore on the
core regression tests, not taken on trust.

## Fix pass — round 1 (Fable, same tier as the build)

Fixed the BLOCKER (`markBroken` no longer touches the install-status
record at all — `plugins.install_state='broken'` is the single source of
truth, and it already was for the chip and the tax check) and all 3
MAJORs (honest tender messaging that distinguishes one-line-blocked from
all-lines-blocked; corrected help docs distinguishing self-heal vs.
manual-reimport recovery; a bounded backoff — 5 attempts, then one attempt
per 10 ticks — tracked in-memory on `common.Deps`) plus all MINORs.

## Independent review — round 2 (Opus, scoped to the fix, worktree-isolated)

Confirmed all four claimed fixes genuinely work — including two of its own
real revert-then-restore proofs — but found a **second, still-open route to
the identical BLOCKER**: `cloudInstallPluginVersion`'s several
lifecycle-only `Save` calls (a `Requested` marker, a classified failure)
never set `PluginID`, and `InstallStatusRepo.Upsert` did a full-column
overwrite — so a **failed** re-fetch attempt on an already-broken plugin
blanked its `plugin_id` and demoted it to `Failed`, reopening the exact
same permanent-un-removability, and reproduced it empirically. This matters
precisely because round 1's new backoff logic makes a persistently-failing
re-fetch the steady state for exactly the plugins most likely to hit it.
Also found: the new self-heal/re-import tally wording had no test coverage,
a grammar bug (plural verb on a single-element list), and a stale backoff
counter never cleared on prune.

## Fix pass — round 2 (Fable)

Two coordinated changes, because TDD showed the identity fix alone wasn't
sufficient (the prune loop also requires `State == Active`, which the
identity fix doesn't address):

- **Repository layer** (`InstallStatusRepo.Upsert`): identity fields
  (`plugin_id`, `plugin_name`) now overwrite only when the incoming value
  is non-blank (`CASE WHEN excluded.x != '' THEN excluded.x ELSE x END` —
  SQLite's `COALESCE` only special-cases `NULL`, not `''`). Protects every
  caller of this method, not just the one found broken, including a latent
  identical risk in the manual plugin-store install handler.
- **Call site** (`cloudInstallPluginVersion`): a hoisted single read of the
  listing's prior record (also deduplicating the pre-existing #495
  prior-good rollback-snapshot logic) decides whether a failed attempt is a
  fresh install (stays `Failed`, as before) or a re-fetch of an
  already-installed plugin (stays `Active`, carrying the prior identity and
  version forward — the failed attempt uninstalled nothing).

Plus the three MINORs: test coverage for the tally's self-heal/re-import
wording, the grammar fix, and clearing the backoff counter on successful
prune.

## Verified beyond automated tests

- **The orchestrator personally re-verified both halves of the round-2 fix
  with its own revert-then-restore**, rather than spawning a fourth
  subagent round (the fix was narrow, well-understood, and the orchestrator
  had already read every line of it): reverting only the SQL-layer fix
  showed identity correctly preserved but `State` still wrongly `failed`
  (prune loop still blocked) — proving the call-site fix is independently
  load-bearing; reverting only the call-site fix (with the SQL fix in
  place) reproduced the reviewer's exact failure signature — proving the
  repo-layer fix is independently load-bearing. Both restored, both pass.
- Full gate run by the orchestrator after every round: `go build ./...`,
  `go vet ./...`, `go test ./... -race` (all packages), and all 5 guard
  scripts (`guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-help-topics.sh`) — all green on the
  final commit.
- A known, pre-existing, unrelated flaky test (`internal/pages`,
  intermittent "no such column: updated_at" from `GetPluginVersionAt`
  against a different test's hand-rolled partial schema — filed separately
  as ut-docs#625) was observed during gate runs and confirmed unrelated by
  re-running clean; not a regression from this diff.
- The Ed25519 verification chain was traced end-to-end by round-1's
  reviewer: the broken-plugin re-fetch path reuses the exact same
  `cloudInstallPluginVersion` → `MarketplaceInstaller.Install` path a manual
  install takes — no new unverified load/copy shortcut.

## Follow-ups filed (found along the way, not silently dropped)

- ut-docs#625 — the pre-existing `seedForPages` test-schema gap (missing
  `updated_at` column) causing the intermittent unrelated flake above.
- NEW-4 from round 2 (nit, not fixed): when *some but not all* basket lines
  are blocked (a shop with two tax plugins, only one broken), the tender
  message says generic "checkout unavailable" rather than naming the
  specific blocked items — never wrong, just less actionable than it could
  be. Left as a follow-up rather than grinding a fourth round on a nit.
- The immediate post-join sync tick (`kick`-triggered rather than waiting
  up to 30s) — `runSyncLoop` already supports this for the journal-push
  side; `StartSyncPull` doesn't use it. Latency polish on top of a
  mechanism that's already correct (fail-closed, not silently wrong) during
  that window — explicitly out of scope for this card.

## Verdict

Safe to merge.
