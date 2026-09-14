# 2026-09-14 — EventBus.Unsubscribe double-close on a multi-event-type shared channel (ut-docs#2242)

## What shipped

Latent bug flagged during ut-docs#1566's dead-code review (a comment on
`Unsubscribe` already named it, unfixed, pointing at this card).

- **`internal/plugins/ipc.go`**: `EventBus.Unsubscribe(pluginID)` iterates
  `eb.subscribers` (event type → subscriber slice) and called
  `close(sub.Channel)` once per matching subscriber entry. `subscribe()`
  registers the *same* `EventSubscriber` (same channel) under every event
  type passed to a single `Subscribe`/`SubscribeWithHandler` call, so a
  plugin subscribed to >=2 event types in one call would panic with
  "close of closed channel" the moment it was unsubscribed. Dormant in
  production because every current caller subscribes with exactly one
  event type. Fixed by adding the same `closed map[chan Event]bool`
  dedupe guard `ResetSubscribers` already uses in this file — declared
  once per `Unsubscribe` call, so it dedupes across however many event
  types the call touches, not just a hardcoded pair.
- **`internal/plugins/ipc_test.go`**: new
  `TestEventBus_Unsubscribe_MultiEventTypeSharedChannel` — subscribes one
  plugin to two event types in a single `Subscribe` call (backed by real
  manifest hooks for both), unsubscribes, and asserts the shared channel
  closes cleanly exactly once.

## Independent review

Sonnet, fresh context.

**What it did:** read the full diff plus the surrounding `ipc.go`
(`ResetSubscribers`, `subscribe`, `Unsubscribe`) and both the existing and
new tests in `ipc_test.go`; checked the dedupe generalizes beyond two
event types (it does — the map is scoped to the whole call, not per
event-type-pair); confirmed the fix only changes *which channels get
closed*, not *which subscribers/event types get cleaned from
`eb.subscribers`* (the `sub.PluginID != pluginID` filter is untouched);
checked locking (unchanged — `eb.mu.Lock()` still held for the whole
body, `closed` is call-local, no new concurrency surface); confirmed the
new test reproduces the bug (verified independently below) and compiles
against existing test helpers.

**Verdict: Approve.**

**Nit (non-blocking):** the regression test has no `recover()`, so
pre-fix it crashes the whole test binary via panic rather than failing
one test cleanly. Acceptable for a regression test proving a real panic
is fixed; not worth a `recover()`-and-`t.Fatal` wrapper for this.

## Verified beyond automated tests

- **Confirmed genuinely red before the fix**: stashed just `ipc.go`,
  reran the new test alone — panics with `close of closed channel` at the
  exact `close(sub.Channel)` call the bug report named, confirming the
  test exercises the real bug and not a tautology.
- **Green after**: same test, fix restored — passes.
- `go test ./internal/plugins/...` (all 4 packages) green, `go vet`
  clean, `gofmt -l` clean on both changed files, `go build ./...` clean.
- No SQL/data-access path touched (`guard-data-access.sh` not
  applicable) and no user-facing string touched (`guard-i18n.sh` not
  applicable) — verified by reading the diff, not just by label: the
  change is confined to an in-memory map/channel bookkeeping fix.
- No UI surface touched (`internal/pages`, `web/`, no plugin
  page/button/theme) — UX step skipped per this pipeline's own rule for
  backend-only changes.

## Deferred

None — this is the complete, scoped fix; no follow-up work was found
along the way.

## Safe to merge

Yes.
