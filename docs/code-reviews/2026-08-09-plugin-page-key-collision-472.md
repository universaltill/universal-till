# Code review: plugin menu/external-API entry keys collide across plugins

**Card:** universaltill/ut-docs#472
**Date:** 2026-08-09
**Complexity:** medium — Dev inline (Sonnet), Review via independent Opus
subagent (isolated worktree), per this pipeline's model-routing rule. One
review round: the round found a real blocker plus several should-fix
findings, all fixed in this same cycle and self-verified (build/vet/tests/
guards + targeted mutation tests) rather than spun into a second full Opus
pass — none of the findings were money/tax/data-loss/security class, the
bar this pipeline's process-depth rule sets for earning a second full
independent round.

## What shipped

`internal/plugins.Manager.MenuPlugins` (built by `loadMenuEntries` from
`ListMenuEntries`'s `type='page'` rows) was keyed by bare entry `key`
across **all** installed plugins:

```go
m.MenuPlugins[mp.Key] = mp
```

Two plugins registering a `type:"page"` entry under the same key silently
overwrote each other — whichever loaded last won, no error, no warning —
and the same namespace backs `GET /ext/{key}` (`internal/pages/
external_api.go`), so a collision there resolved to a predictable *wrong*
route instead of a 404.

The fix extends an already-shipped precedent for a different entry type —
`validatePaymentEntryKeys`/`FindPaymentKeyConflicts` (payment-entry key/
label collisions, ut-docs#16/#168/#363, ADR-0031) — to `type:"page"`
entries, at the same two call sites:

- **`internal/data/plugin_repo.go`**: new `PageKeyConflict` +
  `FindPageKeyConflicts(ctx, tx, pluginID, keys)` — per candidate key,
  checks whether another plugin already owns a `type='page'` entry with
  that key in `plugin_entries`.
- **`internal/plugins/manifest.go`**: new `validatePageEntryKeys` —
  format-validates every `type:"page"` entry key (non-empty, no
  surrounding whitespace, no `':'`, mirroring `validatePaymentEntryKeys`'s
  hygiene checks), then checks cross-plugin conflicts for every key
  **except** `DocsEntryKey` ("docs", ADR-0037). Wired into `PersistManifest`.
- **`internal/plugins/rollback.go`**: `RollbackManager.Rollback` calls the
  same function, so a legacy on-disk manifest predating this check can't
  restore a colliding key either.
- **`internal/plugins/install_status.go`**: `ClassifyInstallError` gets a
  new case mapping a page-key-collision error to
  `plugins.install.error.page_conflict`, `Retryable: false` — same shape
  as the existing `payment_conflict` case.
- **`internal/plugins/plugins.go`**: `loadMenuEntries` now logs when it's
  about to overwrite an existing `MenuPlugins` entry with a different
  plugin's — surfaces a collision that predates this guard (already on a
  till) instead of dropping it silently, mirroring `warnPaymentMethodAnomalies`.
- New locale key `plugins.install.error.page_conflict` in all 4
  `web/locales/*.json` files.
- New tests: `internal/plugins/page_key_validation_test.go` (cross-plugin
  collision rejected + catalog/entries left clean, self-upgrade allowed,
  `docs` exempt across plugins, format validation, rollback rejection),
  plus additions to `install_status_test.go` (classification + locale
  resolution) and `manager_test.go` (shadowed-collision log line).

### The `docs` exemption

`DocsEntryKey` is deliberately exempt from the *cross-plugin collision*
check (not the format checks). Traced independently (by both Dev and the
reviewer): `loadMenuEntries` explicitly skips adding a `docs`-keyed row to
`MenuPlugins`, and the Docs-button feature (`plugins_page.go`) looks docs
routes up **per plugin ID** (`docsRouteByPlugin[e.PluginID]`), not by bare
key — so two plugins both using `key:"docs"` never collide via
`MenuPlugins`/`GET /ext/{key}` in any live code path today, and rejecting
on it would break the ADR-0037 convention every plugin author is asked to
reuse verbatim. This is scoped as an implementation decision for the
`MenuPlugins` namespace specifically — it does **not** resolve ADR-0037's
own "Not decided here" footnote (a different question: a single plugin
declaring two `docs` entries, already blocked by the DB's
`UNIQUE(plugin_id, key)`) — an earlier draft of the code comment overclaimed
this and was corrected during review triage.

## Independent review (Opus, isolated worktree)

Ran the full gate itself (build/vet/gofmt/tests/all 4 CLAUDE.md guards),
independently re-verified the TDD claim by reverting the fix and
confirming the two load-bearing tests fail with the claimed error (the
rollback test especially — log output showed the rollback *succeeding*
without the guard), then restored and confirmed green. Also ran an extra
mutation test on the `docs` exemption branch to confirm it's load-bearing,
not incidentally passing. Verified the `docs`-exemption reasoning above by
reading the three call sites directly rather than taking the diff's
comments on faith, and went a step further than the diff's own claim by
probing a **third** path (`/plugin/{route}` dispatch) the diff didn't
mention.

**Verdict: NOT safe to merge as-is** — one blocker, three should-fix, two
nitpicks.

### Blocker — fixed

`ClassifyInstallError` was not extended for the new error, so a page-key
collision reached the shop owner as a **generic retryable failure**
(`plugins.install.error.retryable`, "Install failed. You can retry.") —
exactly the failure mode ut-docs#169 fixed for the payment branch,
reintroduced here. Worse, on the store-install path (`plugin_api.go`) the
raw error is never logged either, so the actionable message naming the
key and owning plugin existed nowhere the operator or support could see
it. **Fix:** the `ClassifyInstallError` case + locale key described above.

### Should-fix — all folded in this cycle

1. **Pre-existing collisions were still silently dropped with no signal.**
   The install-time guard stops new collisions but does nothing for a
   till that already has one. **Fix:** `loadMenuEntries` now logs which
   plugin got shadowed, mirroring `warnPaymentMethodAnomalies`'s existing
   pattern for the analogous payment case. New test:
   `TestLoadMenuEntries_LogsShadowedPageKeyCollision` (mutation-verified —
   fails without the log line).
2. **Input-hygiene checks from the payment precedent were dropped.** An
   empty, whitespace-padded, or `':'`-containing page key was accepted.
   **Fix:** the same three checks `validatePaymentEntryKeys` already runs,
   applied to every `type:"page"` entry. New test:
   `TestPersistManifest_PageKeyFormatValidation`.
3. **Comment overclaimed resolving ADR-0037's own open footnote.** That
   footnote is a different question (within-plugin duplicate docs
   entries, already prevented by a DB constraint) than the cross-plugin
   `MenuPlugins` namespace decision this diff actually makes. **Fix:**
   reworded the comment on `validatePageEntryKeys` to state the narrower,
   accurate scope and note the ADR footnote stays open.

### Should-fix — deferred, filed as a follow-up card

4. **Docs (and any page entry) can still collide on shared `route`, not
   just `key`.** `internal/pages/plugin_page.go`'s `findPageEntry`
   resolves `/plugin/{route}` by first-row-match, unguarded by anything
   this diff touches — the reviewer demonstrated two plugins both
   registering `route: "/plugin/docs"` installing cleanly, with the
   second's Docs button silently opening the first plugin's content. This
   is the same bug class as #472 in a distinct namespace (`route` vs.
   `key`); reviewer explicitly recommended a separate card over expanding
   this diff. **Filed as ut-docs#499.**

### Nitpick — accepted as-is, not fixed

5. A disabled (but still installed) plugin's page key still blocks a new
   plugin's install, with a message that doesn't say the owner is
   disabled — defensible (re-enabling would recreate the collision) and a
   smaller gap than the payment case's `OwnerInstalled` distinction, since
   a page entry has no orphan-survival state (unlike a retained tender
   row) — an uninstalled plugin's rows are FK-cascaded away entirely.
   Left as a candidate follow-up, not blocking.
6. `FindPageKeyConflicts` is one round-trip per key (N+1) — faithful to
   the payment precedent, negligible at manifest scale. No action.

## Verified beyond the automated suite

- **TDD, both directions, twice.** Dev wrote all four original tests
  red-then-green against the initial implementation; after the review's
  fixes, re-ran the same revert→confirm-fail→restore→confirm-pass cycle
  personally for the two review-driven additions (`ClassifyInstallError`
  case, shadowed-collision log line) rather than trusting the review's
  own re-verification alone.
- **Residue check**: a rejected install leaves zero rows in `plugins`,
  `plugin_entries`, `plugin_catalog`, and `plugin_settings` (reviewer
  probed all four independently; Dev's test now also asserts
  `plugin_catalog` explicitly, closing the reviewer's nitpick that only
  `plugins` was checked).
- **Cross-type false-positive check**: a page key and a button/payment key
  sharing the same literal string do not conflict with each other
  (reviewer probed both directions).
- **Complete write-path coverage confirmed**: `ReplacePluginEntries` has
  exactly two production callers (`manifest.go`, `rollback.go`), and both
  now validate — no bypass path.
- **Translations**: `plugins.install.error.page_conflict` was added to
  `fa`/`tr`/`ar` by structurally adapting the already-shipped, reviewed
  `payment_conflict` string for the same UI surface (substituting "page
  key" for "payment key or label") rather than a fresh model round-trip —
  the self-hosted translation endpoint
  (`http://192.168.1.231:11434`, `reference/translation.md`) is
  unreachable from this cloud pipeline session (private LAN address,
  confirmed by a direct connection timeout). Flagged here rather than
  silently guessed; worth a native-speaker QA pass when someone with LAN
  access is next in the area, same as any machine-adapted string.
- Full `go build ./...`, `go vet ./...`, `gofmt -l` (clean), full
  `go test ./... -race` (all green, no pre-existing failures reproduced),
  and all 4 CLAUDE.md guards (`guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-i18n`) — run twice, once before the
  review and once after folding in its fixes.
- No file writes, no money, no template/page/route touched — the two
  "recurring bug class" checks (`os.MkdirAll` before a file write,
  `paths.Data(...)` vs. a cwd-relative path) are not applicable to this
  diff; confirmed by reading the full diff, not assumed.

## Deferred / explicitly out of scope

- **ut-docs#499** — the `route`-collision namespace (see above).
- Within-manifest duplicate page keys (two entries in the *same* manifest
  sharing a key) stay on the pre-existing raw `UNIQUE(plugin_id, key)`
  SQLite constraint error, unchanged — same accepted gap the payment
  precedent left before ut-docs#363, and out of this card's scope (cross-
  plugin collisions only).
- The disabled-plugin-reserves-its-key nitpick (#5 above).

## Safe-to-merge verdict

Yes, after the blocker and the three should-fix items above were folded
in and re-verified. No real client/shop name used anywhere in tests; no
secret-shaped literal introduced. No manual/`web/help/` update needed —
this change has no user-visible surface (install-time validation only, no
new template/page/route).
