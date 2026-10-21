# Till shows the chain plan as Multi-location (ut-docs#4584)

**Change.** `subscription.plan.chain` is "Multi-location" in every core locale
(en, ar, fa, tr), per the ADR-0060 amendment of 2026-10-17: the plan id stays
`chain`, the display name is a product name and is not translated. README plan
tables and one test fixture message use the new name. No help topic named the
plan.

**Review** (Opus, fresh context, one round): diff correct, no code keys on the
display string (templates look the name up by plan id). Findings: README
headers still said Chain (fixed); a test fixture message said Chain (fixed);
ar/fa/tr still translate the Shop and Pro names, which the ADR says are product
names too (out of this card's scope, filed as a follow-up card).

**Verified.** i18n, i18n-meta, help-topics, help-drift and compliance-claims
guards pass; `go test ./internal/cloudsync/... ./internal/pages/...`.
Language packs de/es/pt carry the same key and get catch-up PRs.
