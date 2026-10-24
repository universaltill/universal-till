# Review — sync the embedded channel order schema with the contract (ut-docs#4899)

Date: 2026-10-24. A data-only change: `internal/cloudsync/testdata/channel-order-v1.json` is replaced byte for byte with `ut-docs/reference/contracts/channel-order-v1.json` (merged in ut-docs#4905). The only difference is `lines.items.name`: `maxLength: 256` and a longer description.

Verified: `TestChannelOrderVendoredSchemaIsCurrent` passes, the whole `internal/cloudsync` package passes, and the schema is the one the contract documents. The host gate already bounds the title at 256 bytes and the intake at 250, so no accepted record changes behaviour (known: ut-docs#4905 section 13.9 lists the three limits that still disagree: 256/250/500).

Reviewed by the builder only (a byte copy of an independently fact-checked contract file). Verdict: safe to merge.
