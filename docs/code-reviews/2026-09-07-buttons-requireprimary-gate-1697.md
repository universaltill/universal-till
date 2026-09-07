# Code review: buttons_api.go reorder/add/remove requirePrimary gate

- **Card:** universaltill/ut-docs#1697 — "buttons_api.go's shortcut_buttons
  mutation routes have no requirePrimary gate (same class as #1689)"
- **Repo:** `universal-till`
- **Complexity:** medium (Dev at Sonnet, Review at Opus per
  `scrum-master`'s model-routing table)
- **Reviewer:** independent fresh-context Opus subagent, isolated
  worktree, did not see the implementation reasoning — read the diff
  cold, ran the repo's own guards live, and independently reproduced the
  TDD claim by disabling the gate and confirming the new test fails with
  a real DB mutation.

## What shipped

`internal/pages/buttons_api.go`'s three mutation routes
(`POST /api/buttons/reorder`, `/add`, `/remove`) mutate `shortcut_buttons`,
which **is** synced shop-wide via `sync_admin_repo.go`'s `adminTables`
(one-way primary-wins pull) — a write accepted on a satellite till used to
be silently reverted on the very next admin pull, the same defect class
already fixed for other tables (`tables_page.go`, `kitchen_stations_page.go`,
`catalog/handlers.go`, see ut-docs#1546/#1590/#1667/#1689 and their own
review records).

- `requirePrimary` closure added, mirroring `catalog/handlers.go`'s
  JSON/fragment pattern (not a full-page redirect, since these routes
  return an htmx fragment or a bare status): checks
  `d.SyncPrimaryURL(r.Context())`, refuses with 409 +
  `common.LocalizedError` before any store call.
- New locale key `designer.error.replica_use_primary` in
  `web/locales/{en,ar,fa,tr}.json`.
- New regression test `TestButtonsAPI_MutationsRefusedOnReplica`
  (`internal/pages/buttons_api_test.go`), mirroring
  `catalog/item_replica_gate_test.go`'s shape: 409 + message + DB-state
  unchanged for all three routes, including `sort_order` for reorder
  (two seeded buttons, codes posted in reversed order, asserts neither
  row's `sort_order` moved).
- `web/help/{en,ar,fa,tr}/till-designer.md`: new bullet mirroring
  `tables.md`/`kitchen-stations.md`'s established "shop-wide, managed
  from the main till" wording. `make docs-shots` run, manifest
  regenerated.

## Review findings

**Correctness (traced independently):** `ButtonStore`'s DB-mutating
methods are `UpdateOrder`, `Add`, `Remove`, `Save` — the first three are
reachable only through the three now-gated routes; `Save` has no
non-test caller anywhere. `/ui/buttons` and `/api/buttons/search` are
correctly left ungated (read-only). No other route in the repo writes
`shortcut_buttons`. Gate runs before any body parsing and before any
store call on all three routes. No gap found in route coverage.

**Blockers found and fixed during review:**

1. **Stale `docs-shots` manifest.** `make docs-shots` had been run
   *before* the final JS edits to `buttons_admin.html` landed, so
   `web/help/img/manifest.json`'s `surface_sha256` still matched `main`
   while the app surface had since changed — `guard-docs-shots.sh` would
   have failed CI. Fixed by re-running `make docs-shots` on the final
   tree and committing the regenerated manifest (surface
   `a59e3bdac080…`).
2. **Raw Go/SQL error newly reachable by the operator.** The JS change
   that broadens `buttons_admin.html`'s `htmx:responseError` listener to
   also cover the remove form (needed so the new 409 refusal is actually
   visible — see below) had a side effect: `ButtonsHTTP.Remove`'s
   pre-existing `http.Error(w, err.Error(), 400)` on a genuine store
   error, previously harmless because htmx discarded the body, now
   paints straight into `#buttons-add-error`. Exactly the class
   `common.LogAndLocalizedError`'s own doc comment (ut-docs#316) forbids,
   and untranslated for an ar/fa/tr operator. Fixed by giving `Remove`
   the same localized-HTML-fragment pattern `Add` already uses
   (ut-docs#1220) — `internal/ui/buttons.go`. Strengthened
   `TestButtonsHTTPRemove_StoreErrorIs400` (`internal/ui/buttons_http_test.go`)
   to assert no raw-error substring and the localized body, matching
   Add's own coverage; TDD-verified (see below).

**Non-blockers fixed during review (cheap, in the exact code being
touched):**

- `persistOrder()`'s promise chain had no `.catch` — a rejected `fetch`
  (till drops off the LAN mid-reorder) would leave `pending` permanently
  rejected, silently disabling every future reorder for the life of the
  page. Added a trailing `.catch` (pre-existing gap, not introduced by
  this diff, but one line to fix while already in this function).
- Locale-string/help-topic terminology drift within the same commit: the
  new `designer.error.replica_use_primary` string used "shortcut
  buttons" wording in ar/fa/tr (copied from `designer.buttons`/
  `search_title`) while the English string and the new help-topic bullet
  (in all four locales) said "quick-sale buttons". Corrected ar/fa/tr to
  reuse the exact "quick-sale buttons" phrasing already established by
  this same commit's `till-designer.md` translations.

**Accepted, not fixed (out of scope / pre-existing, matches established
precedent elsewhere in this codebase):**

- Reorder's move-up/move-down buttons stay enabled on a replica; each
  tap optimistically reorders the DOM, gets refused, then reloads and
  snaps back. Honest (no data loss) but momentarily jarring — the
  established pattern for this defect class is server-side-only
  everywhere else in the codebase too.
- `sessionStorage['buttonsReorderError']` could theoretically survive
  past an interrupted reload (session expiry, offline) and resurface on
  an unrelated later `/designer` visit. Low-probability, low-impact
  (message stays factually true — this till really is a replica), and
  no existing precedent in this codebase handles the equivalent case for
  any other flash-message pattern (`pairing_notice.html`'s dismiss
  fingerprint has the same class of edge case).
- `innerHTML` (not `textContent`) is used for the response body in the
  broadened listener — matches the existing Add-flow pattern exactly
  (which intentionally renders an HTML fragment on a validation error);
  switching only the remove path to `textContent` would be inconsistent,
  and no attacker-controlled data reaches this string (the 409 body is a
  fixed locale string; the other error path, `ParseForm` failure, can at
  most embed a few bytes of a malformed percent-escape from the
  operator's own browser).

## Verified, live, not just read

- `gofmt -l .` — no output.
- `go build ./...`, `go vet ./...` — clean.
- `golangci-lint run ./internal/pages/... ./internal/ui/...` — `0 issues.`
- `go test ./...` (the actual CI/CLAUDE.md gate — no `-race`, which CI
  itself never uses for this repo, see `ci.yml`'s own comment and the
  Makefile's `test-race-pages` target's rationale) — full suite green,
  `internal/pages` 188.665s (matches the #1689 review's own 185-192s
  baseline), `internal/ui` clean, no failures anywhere.
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh`, `guard-compliance-claims.sh`,
  `guard-page-http-error.sh`, `guard-htmx-loaded.sh` — all clean.
- `bash scripts/ci/guard-docs-shots.sh` — clean after the B1 fix above
  (fresh surface hash, all 25 topics × 4 locales screenshotted).
- **TDD re-verification, done twice independently** (once by the
  reviewer subagent on the original diff, once by the orchestrator after
  fixing B2): disabling the `requirePrimary` gate makes
  `TestButtonsAPI_MutationsRefusedOnReplica` fail with a genuine DB
  mutation (`count=1`/`count=0` instead of the refused `0`/`1`); restoring
  it passes again. Separately, reverting the `Remove` localized-fragment
  fix makes the strengthened `TestButtonsHTTPRemove_StoreErrorIs400` fail
  with the raw `"shortcuts.remove_button: sql: database is closed"`
  string actually present in the response body; restoring the fix
  passes again.
- **Manual, driven, visual check** (not just Go-level assertions —
  neither this defect class nor its four prior sibling fixes in this
  repo have any Playwright/e2e coverage, so this matches, not exceeds,
  established practice): a throwaway Playwright spec (not committed)
  mocked the real 409 response for both `/api/buttons/remove` and
  `/api/buttons/reorder` against the actual rendered `/designer` page and
  screenshotted the result. Both show the localized refusal message
  ("This till follows a primary till — manage quick-sale buttons on the
  primary till.") rendered cleanly above the button grid, confirming the
  JS wiring genuinely reaches the operator's screen, not just that the
  code reads as if it should.
- htmx semantics the JS depends on were verified by the reviewer against
  the actual vendored htmx 1.9.12 source (not assumed): a form submit's
  `evt.detail.elt` really is the `<form>` element, `htmx:responseError`
  fires before `htmx:afterRequest`, and `successful` is `false` for a
  409 — so the new "clear on success" listener never wipes a genuine
  refusal.
- Recurring bug classes checked: N/A — this diff adds zero
  `os.MkdirAll`/`os.Create`/`os.WriteFile`/`paths.Data` calls; no
  file-write handler, no cwd-relative path.
- Translations (ar/fa/tr) checked as real, grammatical, natural
  sentences against the existing sibling strings in the same files
  (`tables.error.replica_use_primary` etc.), not just presence-checked.
- No real client/shop name; the only new literal is
  `http://primary.example` (RFC 2606 reserved test domain).
- Git identity on every commit: `Farshid Mirza
  <4035824+farshidmirza@users.noreply.github.com>` (author and
  committer).

## Verdict

**Safe to merge.** Both blockers found by independent review were fixed
in this same branch, re-verified live, before merge. Non-blockers were
fixed where cheap; the three accepted items above are genuinely
out-of-scope/pre-existing and match this codebase's established
precedent for the same defect class elsewhere.
