# Code review: catalog item-form double-submit duplicate row (ut-docs#1365)

## What shipped

`ut-docs#1365`: `web/ui/pages/catalog.html`'s `#item-form-submit` was never
disabled while a save request was in flight. Harmless before ut-docs#1363
(every response re-rendered the whole table), but #1363's row-level HTMX
OOB protocol decides insert-vs-in-place from the item's *previous* active
state, read once before the mutation (`internal/pages/catalog/handlers.go`,
the `wasActive` read in the update handler, which itself documents this as
"reachable for real: deactivate a row while the edit form still holds that
item, then save"). Two rapid clicks while reactivating a formerly-inactive
item both read `wasActive=false` before either write lands, so both
responses emit a `beforeend` row insert instead of the second one updating
the row the first just created — two DOM elements sharing the same id,
until the page reloads.

**Fix** (`complexity:easy`, one file, no new architecture):
- `submit.disabled = true` set synchronously in the `submit` handler,
  before `htmx.ajax(...)` fires.
- Re-enabled as the first statement of both `.then()` (covers success and
  every server-answered error — per the existing ut-docs#917 comment,
  `htmx.ajax()`'s promise resolves on any completed HTTP response, error
  statuses included) and a new `.catch()` (the network-level-failure path
  that actually rejects the promise — without this the button would stay
  disabled forever after e.g. a dropped connection).
- `if (submit.disabled) return;` belt-and-braces guard at the top of the
  handler for any non-click submission path.
- New e2e spec `e2e/tests/catalog-reactivate-double-submit-1365.spec.ts`:
  reproduces the exact reactivation precondition (create → load into form
  → deactivate via the row's own button, without touching the form → two
  synchronous `.click()` calls), holds the `/api/catalog/item/update`
  response via `page.route` so both clicks are dispatched before either
  request resolves, and asserts exactly one request reaches the server and
  exactly one row exists afterward. A second test covers the general "the
  submit button is disabled for the duration of a save" UX property with a
  held `/api/catalog/item` response.

## Review

Independent review by a fresh-context Sonnet subagent (`complexity:easy`
routing — Dev and Review both ran on the session model, review as an
independent instance that never saw the dev reasoning). Read-only, no
files modified.

**Verdict: no blocker or major issues.** The reviewer traced every settle
path of the `htmx.ajax()` promise (success, HTTP 4xx/5xx, network
failure), confirmed the race closure is real (a disabled `<button>`'s own
`click()` never dispatches per the HTML spec, so the test's synchronous
double-click genuinely proves the guard, not a timing coincidence),
confirmed no double-notice/listener-leak interaction with the existing
module-level `htmx:responseError` listener, confirmed the reused
`NOTICE_MSG.itemFormErrorGeneric` (`catalog.error.server`) wording matches
the identical `.catch()`-reuses-the-generic-key convention already used a
few dozen lines below for the image-upload form, and grepped the repo to
confirm nothing else programmatically submits `#item-form` or clicks
`#item-form-submit` that this could affect. Also confirmed the four
existing catalog e2e specs that touch this button (`catalog-row-oob-1363`,
`catalog-save-notice-917`, `catalog-active-checkbox-1367`,
`osk-decimal-...-1284`) all `await` the success notice before their next
interaction, so none rely on the button being clickable mid-flight.

Two non-blocking notes:

- **Nit (not changed)** — the inline comment's example for the
  belt-and-braces guard ("implicit Enter-key submit") may not actually be
  reachable, since browsers generally also refuse implicit submission via
  a disabled default submit button. Low-confidence, browser-version-
  dependent, and the guard itself is free insurance either way — left as
  written rather than churn the comment for an unverified claim in either
  direction.
- **Scope note (accepted, not fixed here)** — this is a client-side UX
  mitigation for one operator's double-click in one tab, which is exactly
  what ut-docs#1365 describes and what its acceptance criteria ask for.
  The server-side `handlers.go:355-357` read-then-write (`repo.GetItem`
  then `pos.UpdateItem`) is still two unsynchronized steps with no
  transaction/optimistic lock between them, so two *genuinely* concurrent
  requests from different tabs/sessions (not a single operator's double-
  click) could still race through the same window. Out of scope for this
  card; filed as ut-docs#1399 for a future look rather than silently
  dropped.

## TDD verification (independently re-run, red then green)

```
$ git stash push -- web/ui/pages/catalog.html   # revert just the fix
$ npx playwright test --project=default catalog-reactivate-double-submit-1365.spec.ts
  ✘ rapid double-submit ... does not duplicate its row
      Expected: disabled / Received: enabled (toBeDisabled() failed)
  ✘ the submit button is disabled for the duration of a save request
      Expected: disabled / Received: enabled (toBeDisabled() failed)
  2 failed — for the expected reason (no disable/enable logic present)

$ git stash pop                                  # restore the fix
$ npx playwright test --project=default catalog-reactivate-double-submit-1365.spec.ts
  ✓ 2 passed (11.0s)
```

## Full gate

```
gofmt -l .                                        → empty (no Go changed)
go build ./...                                     → clean
go vet ./...                                       → clean
go test ./internal/pages/catalog/...               → ok, 0.459s
bash scripts/ci/guard-data-access.sh                → pass
bash scripts/ci/guard-kiosk-engine.sh               → pass
bash scripts/ci/guard-plugin-menu-read.sh           → pass
bash scripts/ci/guard-i18n.sh                       → pass (1338 keys, all locales match en.json —
                                                        diff adds zero new user-facing strings, reuses an existing key)
bash scripts/ci/guard-compliance-claims.sh          → pass
bash scripts/ci/guard-e2e-fixtures-import.sh        → pass (68 specs, all import from ./fixtures)
bash scripts/ci/guard-htmx-loaded.sh                → pass
bash scripts/ci/guard-autofill-suppression.sh       → pass
npx playwright test --project=default catalog      → 26/26 passed (48.9s) — full catalog e2e
                                                        suite, no regressions
```

No SQL, no data-access-layer change, no new locale key, no kiosk-engine
touch, no compliance-wording change — this diff is confined to
`web/ui/pages/catalog.html`'s inline `<script>` plus the new e2e spec.

## Safe-to-merge verdict

**Yes.** No findings required a fix. Full gate green, TDD claim
independently re-verified red-then-green, no regressions in the existing
catalog e2e suite.

## Explicitly deferred

- The scope note above — cross-session/cross-tab concurrent-request race
  at the `handlers.go` level, not closable by a client-side fix — filed as
  ut-docs#1399.
