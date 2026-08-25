# Code review: per-line discount silently reset by a quantity change (ut-docs#971)

**Branch:** `fix/971-line-discount-qty-reset`
**Reviewer:** independent Sonnet subagent, fresh context (complexity:easy →
fresh-context Sonnet review, per scrum-master's model routing) · **Author:**
Sonnet (this pipeline cycle)

## What shipped

On the cashier sale screen, setting a per-line discount and then changing
that same line's quantity silently cleared the discount back to `0`, with
no error shown to the operator.

**Root cause:** the per-line discount box (`web/ui/partials/basket.html`)
redisplayed its value via `{{ .LineDiscount }}`, which invokes
`money.Money`'s `String()`/`Format()` and renders the major-currency-unit
form (e.g. `0.30`). The quantity input on the same basket row carries
`hx-include="closest tr"`, so changing quantity re-submits the whole row,
including the discount box's current (major-units) value. The handler at
`POST /api/pos/line` (`internal/pages/pos_api.go`) expects the `discount`
field in **minor units** and does a bare `strconv.ParseInt`, which fails
silently on `"0.30"` and falls back to treating the discount as `0` — no
error surfaced to the operator or logged.

- **Fix:** `web/ui/partials/basket.html`'s `disc-input` now redisplays
  `{{ .LineDiscount.Minor }}` (the integer minor-units accessor) instead of
  the money-formatted string, so the box always shows exactly what the
  handler expects on the next post. One line changed. No behavior change to
  the initial cashier-typed discount-entry convention (already minor
  units) — only the redisplay/round-trip.
- **Regression test:** `TestLineHandler_QtyChangeDoesNotClearDiscount`
  (`internal/pages/pos_api_test.go`) — handler-level, real HTTP request
  through the real mux (`posPostForm`, matching the file's existing
  pattern). Sets a line discount, extracts the disc-input's redisplayed
  value from the rendered HTML via regexp, re-submits it exactly as a
  browser's `hx-include` would on a quantity change, and asserts the
  discount survives. TDD-first: confirmed failing pre-fix (see below).
- **Manual:** `web/help/en/sell.md` updated — removed the now-false "set
  quantity first, discount last" workaround note, and corrected the
  sentence describing what the discount box redisplays (was: "the normal
  way (e.g. `0.30`)"; now: "keeps showing that same smallest-unit amount
  (e.g. `30`)").
- **Screenshots:** `make docs-shots` regenerated (`web/help/img/**` +
  `manifest.json`) since `web/ui/**` and topic markdown changed;
  `guard-docs-shots.sh` was red before the regen, green after.

## Independent review — single round, no fixes needed

Full independent pass (different Sonnet instance, fresh context, isolated
worktree):

- **Correctness confirmed by reading `internal/money/money.go`**:
  `Minor()` returns the raw `int64`; `Format()`/`String()` produce the
  two-decimal major-unit string. `.LineDiscount.Minor` now emits exactly
  the bare-integer form `strconv.ParseInt` expects — round-trips correctly
  through repeated `hx-include`d posts, and doesn't touch the initial
  cashier-entry convention.
- **No other call site** relies on the old major-unit redisplay: the only
  other `.LineDiscount` template reference is `web/ui/pages/journal_detail.html`,
  which explicitly uses the `money` formatting func in a read-only display
  — unrelated and unaffected.
- **TDD re-verified independently, not taken on trust**: reverted just the
  one-line `basket.html` fix (kept the new test) and reran the test —
  failed with `expected line discount to survive the quantity change at
  20, got 0 (redisplayed value was "0.20")`, matching the reported bug
  exactly. Restored the fix — test passed again.
- **Full gate, run independently**: `gofmt -l` clean; `go build ./...`
  clean; `go vet ./...` clean; `go test ./internal/pages/... ./internal/money/...`
  and the full `go test ./...` both green; `guard-data-access.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh` all pass.
- **File-write bug classes checked explicitly** (missing `os.MkdirAll`;
  cwd-relative path instead of `paths.Data(...)`): not applicable — this
  diff performs no new file writes at all (confirmed by grep, not
  assumed).
- **Manual prose checked in full context**: accurate to the new behavior;
  nothing else in `sell.md` left stale by this change.
- **UI surface**: the `basket.html` diff is exactly the one attribute
  expression — no markup/CSS touched, zero RTL/layout risk. Noted as a
  side benefit: the redisplayed value is now always a clean integer
  consistent with the input's existing `step="1"`, where before it could
  redisplay a value (`"0.30"`) that violated its own step attribute.
- No real client/shop name; no secret-shaped literals.

**One nit, not a blocker, out of scope for this PR:** the `fa`/`ar`/`tr`
translations of `sell.md` are already missing several sections present in
`en`, including the per-line-discount section this fix touches — a
pre-existing translation-lag gap, unrelated to and not worsened by this
change (content hashes for those locales didn't change;
`guard-help-topics.sh` passed).

## Verdict

**Safe to merge.** No blockers, no should-fix items found. Root-cause
understanding confirmed correct by independent code reading; fix is
minimal (one line) and confirmed correct; regression test independently
re-verified red→green; full local gate green.

## Explicitly deferred

- Translating `sell.md`'s per-line-discount section (and the rest of its
  existing gaps) into `fa`/`ar`/`tr` — pre-existing, out of scope here.
