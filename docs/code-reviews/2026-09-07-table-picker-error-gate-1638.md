# Review: table-picker close-gate matches New Sale/New Customer (ut-docs#1638)

## What shipped

`web/ui/partials/table_picker.html`'s `.table-picker-clear` and
`.table-picker-option` buttons used to close `#table-modal` via a plain
`onclick="document.getElementById('table-modal').close()"` that ran
**synchronously on click** — before their `hx-post="/api/pos/table"` request
even started, let alone completed. Same antipattern ut-docs#1624 already
fixed for New Customer/`#payment-overlay` in `web/ui/pages/index.html`, and
flagged as a follow-up candidate in that review.
`internal/pages/pos_api.go`'s `/api/pos/table` handler can legitimately
answer a lost table-claim race with an inline error toast
(`ToastMessage = "basket.table.occupied"`, `ToastLevel = "error"`) instead of
assigning the table — the old onclick closed the dialog regardless of that
outcome.

Fix: both buttons' `onclick` is replaced with the exact
`hx-on::after-request` gate shape New Sale/New Customer already use,
retargeted at `#table-modal`:

```
hx-on::after-request="if (event.detail.successful) { var t = document.getElementById('toast-message'); if (!(t && t.classList.contains('error'))) { var d = document.getElementById('table-modal'); if (d && d.open) d.close(); } }"
```

`hx-post`/`hx-target`/`hx-swap`/`hx-sync` on both buttons are untouched.

New spec `e2e/tests/table-picker-error-gate-1638.spec.ts` (3 tests):
happy path for `.table-picker-option`, a regression test for the actual bug
(premature synchronous close), and happy path for `.table-picker-clear`.

`web/help/img/manifest.json`'s `surface_sha256` was regenerated (`make
docs-shots`, 100/100 shots passed) because the comment/attribute text in
`table_picker.html` changed the file's content hash, same as #1624's PR —
no screenshot actually changed on the happy path (three unrelated PNGs that
`make docs-shots` also touched, from normal rendering non-determinism, were
reverted to keep this diff minimal).

## Important structural difference from #1624 — read before assuming this is a copy-paste

`#table-modal` is rendered **inside** `#table-picker`, which is itself
inside `#basket` (`web/ui/partials/basket.html:88`). So **any** response to
`/api/pos/table` — success or the `basket.table.occupied` error — replaces
`#basket`'s entire outerHTML, which necessarily tears down the live
`#table-modal` node (it reappears, closed, only once the fresh
`#table-picker`'s own `hx-trigger="load"` GET resolves). `#payment-overlay`
in `#1624` is a documented **sibling** of `#basket`, not a descendant, so it
survives a `#basket` swap untouched — genuinely different from this dialog.

Consequence: the gate here **cannot** make `#table-modal` visibly "stay
open through an error" the way `#payment-overlay` does — no `hx-preserve`,
no morph swap exists anywhere in these files. What the gate *can* fix, and
the only thing it changes, is the dialog closing **prematurely** —
synchronously, on click, before the request is even sent. That is the real
bug: an operator could see the dialog vanish instantly regardless of
network latency or outcome, with no correctness signal yet available. The
new spec's regression test asserts exactly that (dialog still open while
the response is deliberately held in flight), not that the dialog survives
an eventual error.

`web/help/en/sell.md`'s existing prose ("the dialog closes on its own...
if a table was taken in the meantime the till says it's already occupied
and keeps your current choice") never claims the dialog visibly stays open
through an error — "closes on its own" stays literally true either way, so
no manual update was needed, matching #1624's own conclusion for the
identical fix shape.

## Independent review (different session, fresh context, worktree-isolated)

**Verdict: SAFE TO MERGE.** No blocking findings.

- Confirmed the structural claim above by direct inspection: `basket.html`
  line 88 nests the placeholder span inside `#basket`;
  `pos_api.go:690-748`'s `/api/pos/table` handler has exactly one exit path
  (`basketView.Render` at line 747) reached from every branch — every
  response is a full `#basket` re-render whose `#table-picker` starts as
  the empty placeholder, not inline dialog content. No `hx-preserve`/morph
  anywhere in these files.
- **TDD re-verification, independently reproduced**: extracted the true
  pre-fix blob (`git show HEAD~1:web/ui/partials/table_picker.html`),
  confirmed it differs from the fix by only the two `onclick`→
  `hx-on::after-request` swaps and the added comment, copied it over the
  live file, and re-ran the spec — test 1 and test 3 passed, **test 2
  failed** with the exact claimed error (`#table-modal` "hidden", not
  visible, 5000ms timeout: the synchronous `onclick` had already closed it
  before the deliberately-held response was released). Restored the fix,
  confirmed `git status` clean, re-ran: all 3 passed again.
- Ran the full gate independently and got real, not assumed, green:
  `gofmt -l .` clean, `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` → `0 issues.`, `go test ./...` full suite green
  (`internal/pages` 185s, `internal/plugins` 92s, `internal/data` 48s,
  etc.), `guard-i18n.sh`/`guard-docs-shots.sh`/
  `guard-e2e-fixtures-import.sh`/`guard-help-topics.sh`/
  `guard-data-access.sh`/`guard-kiosk-engine.sh`/
  `guard-compliance-claims.sh` all ✓.
- File-write/`os.MkdirAll`/`paths.Data` bug classes and raw-SQL-outside-
  `internal/data` are N/A — no Go file is touched by this diff at all
  (only the HTML partial, the new e2e spec, and the regenerated manifest
  hash), confirmed by `guard-data-access.sh` and by inspection.
- No new user-facing strings (`guard-i18n.sh` clean); `reference/
  ux-guidelines.md` checklist satisfied (no new hardcoded colors/spacing,
  no new modal blocker, RTL unaffected — no layout change, pattern reused
  verbatim); no real client/shop name, no secret-shaped literals (test
  table label is the obviously-synthetic `"E2E Picker 1638"`).

**Two non-blocking observations, named for the record, not defects:**

- `.table-picker-clear` always POSTs `table_id=""`, which in
  `pos_api.go`'s switch can only reach the `tableID == current`
  short-circuit or the explicit `tableID == ""` clear branch — it can
  never reach the `default:` claim/occupied branch that sets the error
  toast. Its gate's error-toast check is therefore unreachable in
  practice for that button specifically; harmless, and keeping the same
  gate shape as `.table-picker-option` (which *can* hit that branch) is
  the right call for consistency over hand-tuning two near-identical
  buttons differently.
- For both buttons, the `!(t && t.classList.contains('error'))` half of
  the gate is effectively inert for `#table-modal` specifically (unlike
  the `#payment-overlay` case it's copied from), because by the time
  `htmx:afterRequest` fires, the old dialog node is already gone via the
  `#basket` swap either way — confirmed by the passing/failing test
  behavior above, not just theory. It still correctly matters for the one
  case where no swap happens at all: a genuine non-2xx/network failure
  (`event.detail.successful === false`), where the real dialog legitimately
  survives and the gate correctly leaves it open.

## Commands run and results (implementer, before independent review)

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `golangci-lint run ./...` — `0 issues.`
- `go test ./...` — full suite green.
- `bash scripts/ci/guard-data-access.sh` / `guard-kiosk-engine.sh` /
  `guard-plugin-menu-read.sh` / `guard-page-http-error.sh` /
  `guard-i18n.sh` / `guard-compliance-claims.sh` / `guard-help-topics.sh` /
  `guard-webkit-version.sh` / `guard-kiosk-launch-flags.sh` /
  `guard-android-status-address.sh` / `guard-android-i18n.sh` /
  `guard-emoji-font.sh` / `guard-htmx-loaded.sh` /
  `guard-autofill-suppression.sh` / `guard-e2e-fixtures-import.sh` /
  `check-brand-assets.sh` / `guard-makefile-version.sh` — all ✓.
- `bash scripts/ci/guard-docs-shots.sh` — failed once (content hash
  changed by the comment/attribute edit), fixed by `make docs-shots`
  (100/100 shots passed), then ✓.
- `cd e2e && npx playwright test table-picker-error-gate-1638.spec.ts
  new-customer-error-keeps-overlay-open-1624.spec.ts
  tables-tap-to-add-1025.spec.ts tables-keyboard-reposition-826.spec.ts
  --project=default` — **12 passed**, 0 failed.
- TDD re-verification (implementer's own pass, later independently
  reproduced by the reviewer as above): reverted only
  `web/ui/partials/table_picker.html` via `git stash`, re-ran the new
  spec — test 2 failed exactly as expected (`#table-modal` hidden), tests
  1 and 3 passed. Restored via `git stash pop`, re-ran: all 3 passed.

## Verdict

**SAFE TO MERGE.** No blocking issues from either pass. The fix is a
narrow, verified match to an established pattern used elsewhere in the same
codebase; the regression test provably discriminates old vs. new behavior
(independently reproduced twice); no CI guard, lint, vet, build, or full
test-suite regression.

## Deferred / out of scope

Nothing further identified as a follow-up from this diff.
