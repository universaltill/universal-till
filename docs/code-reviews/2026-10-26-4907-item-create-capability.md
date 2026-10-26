# Review: `item_create` channel capability (ut-docs#4907)

## What shipped

- `internal/plugins/manifest_channel.go`: `item_create` appended to `channelCapabilityEntities`, so a channel manifest may declare `"item_create": "push"` (also `pull`/`both`; the cloud reads only push). Absent means the adapter cannot create products.
- `internal/plugins/manifest_channel_test.go`: `TestParseManifest_ChannelItemCreateCapability` (push, pull, both accepted and kept). Before the fix it failed with `unknown capability "item_create"`.
- `sdk/plugin/channel/abi.go`: `CapItemCreate = "item_create"` after `CapCompareAt`.
- No docs under `docs/` or SDK README list the capability set; nothing else to update.
- Companion changes: ut-cloud (refuses to publish an item not yet at the channel with 409 `create_not_supported`; sweep never queues a create; Review entry otherwise) and ut-docs (`reference/contracts/sales-channel-v1.md`, `reference/plugin-manifest.md`, ADR-0169 list).

## Review

Independent review by Opus 5.5 (fresh context) of the Sonnet-built change across ut-cloud, universal-till and ut-docs; one round, fixes re-verified.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Listings whose create was dead-lettered before this change were remembered as evaluated (`dead:<hash>` counts as in line), so the sweep's evaluated-version skip never reached the new check: no Review entry for exactly the listings the card is about. Reproduced with a scratch test. | Fixed: the skip is bypassed for a published unlinked listing on a channel without `item_create`, or one with an open entry. Regression test `TestTheSweepListsALegacyDeadLetteredUnlinkedListing`. |
| 2 | major | my. allow-lists (`CHANNEL_ERROR_CODES`, `REVIEW_KINDS`, `REVIEW_REASONS`) don't know `create_not_supported`: the 409 shows the generic error and the kind shows raw. | Out of this diff (ut-my-shop): ut-docs#5072, raised to p2. |
| 3 | minor | A legacy dead listing whose channel gains `item_create`: the entry closed but the create was never queued (the `dead:` hash still counted as in line). | Fixed: closing for that reason clears the dead hash; test `TestAChannelThatGainsItemCreateQueuesTheCreateOfALegacyDeadListing`. |
| 4 | minor | Entries stayed open forever when the sweep returns early (channel stops pushing items, store becomes till-master). | Fixed: one bulk `in_sync` close in that branch; test `TestAnOpenEntryClosesWhenTheSweepReturnsEarly`. |
| 5 | minor | Accepted ADR-0169 body edited in place. | Fixed: ADR reverted; the list lives in the contract doc. |
| 6 | minor | Missing tests: purged item's entry, `both`/`pull` directions. | Added (`TestAPurgedItemsEntryIsClosed` fails when the purge clause is removed; `TestItemCreateBothCountsAsPushAndPullDoesNot`). |
| 7 | nit | api-reference said `item_create: push`; `both` also counts. | Fixed. |
| 8 | nit | A dismissed entry is re-raised every sweep while the cause holds. | Accepted: same rule as `over_limit`; the owner's way out is to unpublish or link the item. |
| 9 | nit | Two concurrent sweeps of one connection can collide on the entry's unique index. | Accepted: pre-existing pattern (`overLimit`); one sweep fails and the next succeeds. |
| 10 | nit | fa-IR used «واردکردن» where the import strings use «درون‌ریزی». | Fixed. |

Also found while scoping (filed, not in this diff): an owner-visible signal for other dead-lettered listing operations (`invalid_price`, currency/tax refusals) — ut-docs#5073.

**TDD re-verified by the orchestrator:** with the SetPublished guard and the skip bypass reverted, `TestPublishingANewItemOnAChannelThatCannotCreateIsRefusedBeforeAnythingIsWritten` failed (`publish = <nil>`) and `TestTheSweepListsALegacyDeadLetteredUnlinkedListing` failed (`Refused:0 ops=0`); restored, the package passes.

**Gate:** ut-cloud `scripts/ci/verify.sh` exit 0; `go test -race ./internal/channelsync/... ./pkg/manifest/...` and `-race -run 'Channel|Sync|Listing' ./internal/api/` ok. universal-till `internal/plugins` channel tests and `sdk/plugin` tests ok; gofmt, vet, golangci-lint clean. `ent` regenerated output matches `go generate`. No UI surface in these repos (my. is #5072), so no visual attestation.

**Verdict:** safe to merge. Merge order: universal-till and ut-docs (validator + contract), then ut-cloud.
