# Review — plugin guidelines: invented example repos, links and contacts (ut-docs#5677)

Date: 2026-11-06 · Lane: lane:cloud-24 · Branch: `docs/5677-plugin-examples-link`
Companion PR: ut-cloud `docs/5677-devguide-claims` (full record of the card: ut-cloud `docs/code-reviews/2026-11-06-devguide-claims-5677.md`).

## What shipped

- `docs/plugin_guidelines.md`: links to the non-existent
  `universaltill/plugin-examples`, `plugin-stripe`, `plugin-csv-export`,
  `plugin-shopify`, `plugin-escpos` repos now point at the real SDK
  (`universal-till/tree/main/sdk/plugin`); invented docs/api/forum/Discord
  links removed; contact is the allow-listed `support@` address and the
  universal-till issue tracker.

## Review

Independent reviewer: a different model from the Sonnet author (Opus).
Verdict: approve with fixes. Every new sentence checked against code.

| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | medium | till guide footer still "plugins@… or ask in Discord" | fixed (companion PR) |
| M2 | medium | till guide "Official Example Plugins" lists repos that don't exist | fixed: section points at `sdk/plugin` (companion PR) |
| L1 | low | response-schema enum not pinned | fixed: test extended; red with `community` re-added, green after |
| L2 | low | SLA start point unstated (`service.go:138` counts from assignment) | fixed |
| L3 | low | "official is not stored" slightly wrong | fixed: "those rows store `unverified`" |
| L4 | low | phrase list is a substring match, could block a true future sentence | accepted; narrow an entry if it happens |
| L5 | low | smoke run is trivial for non-WASM plugins | accepted (optional) |

Out of scope → Backlog cards ut-docs#5703 (openapi `Plugin.trust_tier` vs the API's
`trust_level` field name and the fast-lane approver not documented); further
invented claims in universal-till `docs/plugin_guidelines.md` → ut-docs#5704
(`universaltill/plugin-sdk` import paths/packages, 20% commission,
Python/JavaScript, `plugins.universaltill.com`).

## Verification

- TDD: all seven phrases and the enum test failed on main's docs, pass after
  (Dev, re-verified by Tester in a separate worktree and by the reviewer with
  `go test -overlay`).
- `go vet`, `gofmt -l`, `go test ./internal/api/ ./internal/downloads/`
  green; `guard-mail-addresses.sh` green (both repos).
- No visual surface touched (docs and tests only).

Pre-flight: review record — updated; platform/feature claim surfaces —
n/a (this removes claims, adds none; README/website/help make none of these
claims); other rows n/a (no plan, en.json, ADR, UI, manifest change);
follow-ups → Backlog cards (above).
