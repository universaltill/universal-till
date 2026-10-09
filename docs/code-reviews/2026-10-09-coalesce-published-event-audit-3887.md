# Review — coalesce audit rows for plugin-published events (ut-docs#3887)

**Date:** 2026-10-09 · **Lane:** lane:cloud-54 · **Built by:** Sonnet (complexity:easy) · **Reviewed by:** Opus 5.5, fresh context

## What shipped

`EventBus.EnqueuePublished` (the delivery path for `event_publish`, ADR-0121 §3,
hop > 0) no longer writes one `event_published` row per event plus one
`event_dispatch` `enqueued` row per subscriber. A plugin publishing at its
full 20/s could write ~1.7–3.5 M rows a day into the `audit_log` that also
holds the journal entries. Now:

- no per-subscriber `enqueued` row for a published event;
- one `event_published` row per publishing plugin per minute
  (`publishedAuditInterval`), carrying `plugin_id` and `coalesced=N` — that
  plugin's publishes (any type) since its previous row that got none;
- one `denied` row per subscriber + event type per minute, with the same count;
- `dropped` (channel full) unchanged; core hop-0 `Publish` unchanged.

`EnqueuePublished` now takes the publisher's plugin id (`flushPublished`
passes `hs.pluginID`). Throttle state sits under its own mutex — callers hold
`eb.mu.RLock`, nothing re-takes `eb.mu`. Doc: ut-docs
`architecture/wasm-runtime.md` (Event publish bullet).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | Comment + doc claimed coalesced counts are "never lost"; a trailing burst with no later publish (quiet, disabled, restart) is never counted. | Fixed: wording now states the gap honestly (first row of a burst is always written, so the stream stays visible). Flushing on disable/shutdown judged not worth its code for a p3 volume bound. |
| 2 | minor | The per-publisher row names one event type but counts publishes of every type. | Fixed: row text and doc say "publishes of any type by this plugin". |
| 3 | minor | `denied` row said "coalesced" but carried no count. | Fixed: per-key counter, `coalesced=N earlier denials`; test asserts `coalesced=49` (red on the pre-fix code, green after). |
| 4 | nit | Throttle maps never pruned. | Accepted: keys bounded by installed publishers and registered subscriptions (a plugin cannot grow them by inventing types); same as `dropWarnedAt`. |
| 5 | nit | Coalesced row timestamped with the injectable clock. | Accepted: `time.Now` in production. |

## Verified

- TDD re-verified by the reviewer in a separate worktree: on origin/main code
  `TestEnqueuePublishedCoalescesPublishAudit` fails (`event_published rows = 100, want 1`),
  `TestEnqueuePublishedCoalescesDeniedAudit` fails (`denied rows = 50, want 1`);
  the dropped and core-publish regression guards pass both ways.
- The denied-count assertion added after review: red on the review-time code
  (`no denied row carries coalesced=49`), green after.
- `go test ./internal/plugins/ -count=1` full package: ok (568 s);
  `-race` on the event-publish/enqueue tests: ok. `gofmt`, `go build ./...`,
  `go vet`, data-access guard clean. Locally `guard-deadcode-baseline.sh`
  (deadcode built with an older Go) and `guard-shellcheck-version.sh`
  (no shellcheck) could not run — CI covers them.
- Backend only: no UI surface, no locale keys, no manual topic affected.

**Verdict:** safe to merge.
