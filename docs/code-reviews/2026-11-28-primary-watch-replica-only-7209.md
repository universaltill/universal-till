# Review — PrimaryWatch writes replica-only after a promote (ut-docs#7209, slice 1)

**Scope.** `internal/discovery/primary_watch.go`, `primary_watch_test.go`,
`primary_watch_promote_test.go` (new); ut-docs `architecture/lan-sync.md`
(separate ut-docs PR). Built by Sonnet; reviewed independently by Opus 5.5;
Tester a separate agent.

**What shipped.** `NewPrimaryWatch` now takes `PrimaryWatchSettings{Get,
SetIfReplica}` (no `Set`), so an unguarded write no longer compiles;
`JoinWatch`'s `WatchSettings` is untouched. `pinWrites` replaces `storePin` /
`clearNextPin`. `rediscover` (url + till id + pin + next pin `""`), the
`ContactOK` id/pin backfill and `Reproof` each commit with one `SetIfReplica`
(#5903). On `data.ErrNotReplica` each logs one INFO line; `ContactFailed`
returns a zero outcome (no relink, no `primary_relinked` audit, no redial, no
"main till unreachable" warning) and `Reproof` returns nil without setting the
cooldown. Clearing the next pin writes `""`, which every reader (peer.go,
sync_link_client.go, app.go) already treats as absent.

**Findings.**
- M1 (medium, fixed): the warning suppression was untested — `relink()` returns
  only the last `ContactFailed` outcome, and the warning fires on the first.
  Both rediscover tests now check every call (`relinkEvery`); mutation
  `return out` now fails both (verified by the orchestrator and the Tester).
- L1 (low, fixed in the ut-docs PR): "the only writer" → "the only background
  writer" (join also writes `sync.primary_url`); follow-up card named.
- L2 (low, accepted): `promoteOnGet` counts reads; a new read in `challenge`
  could move the promote point. The Reproof test still asserts the end state.
- L3 (nit, accepted): `warned`/`failures` stay set after a refusal; harmless,
  the promoted till stops calling the watch.

**Verification.**
- Red first: with `primary_watch.go` at origin/main the new tests compile and
  5 of 6 `_7209` tests fail (url/id/pin written, `IsReplica` true, 0 INFO
  lines, 0 `SetIfReplica` calls); `PromotedDuringReproofProof_7209` passes on
  both, as a regression test (the line-387 re-check already covers it).
- Mutations: 12 tried by the Tester, 11 caught; the 12th was M1, now caught.
- Gate: gofmt, `go build ./...`, `go vet`, golangci-lint (discovery) clean;
  `go test -race` discovery ok; pages full package ok without race, race
  subset (PrimaryWatch/pin/TLS) ok — full `-race` pages exceeds 55 min on the
  4-CPU box; data/settings ok. Guards: data-access, fixed-dates, core-neutral,
  i18n pass (catimport-version needs tags; `catimport/` unchanged).

Pre-flight: help topic n/a (no user-visible behaviour, log lines only);
reference doc updated (ut-docs `architecture/lan-sync.md`); en.json n/a;
README n/a; ADR n/a (reuses #5903); follow-up → ut-docs#7248 (link client,
plugin-records contact stamp).

**Verdict.** Approve after M1 — done.
