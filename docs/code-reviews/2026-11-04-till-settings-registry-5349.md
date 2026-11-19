# Till settings registry, slice 1 (ut-docs#5349, part of #4504)

Reviewer: Opus 5.5 sub-agent (code written by Sonnet 5.5). Roles run: scrum-master (card pre-claimed), ba,
architect, dev (TDD), tester, reviewer.

## What shipped
- `data.AgentSettings` is now the typed registry: Type, Tier (every entry, test-enforced), Secret, Min/Max/Unit,
  Options, MinTillVersion, RemoteWritable, Page. 27 entries (7 added: printer receipt policy, receipt header 1-3 and
  footer, invoice address and VAT number).
- `allowedRemoteTillSettingKeys` and the read-only reported keys are derived from it (RemoteWritable and tier
  normal|sensitive). The same 20 keys stay remote-writable (pinned by a test).
- `cloudSetTillSetting` validates bool/int/enum from the registry; special rules (receipt-policy country plugin,
  browsing mode, order type, barcode set, locales) stay in code; an entry type without a rule is refused.
- `set_till_setting` accepts optional `expect` (compare-and-set; "changed since ..." refusal, nothing written;
  absent = old behaviour). Needs a till >= `RegistryMinTillVersion` (0.35.4: release.yml defaults to a patch bump) to honour it.
- Heartbeat report built from the registry: secret tier only "set"/"not set", never/tierless invisible.
- Generator `make gen-settings-registry` -> `docs/till-settings-registry.json`; a data test fails when stale.
- Tests: tiers present, registry-only keys listed, every `settings.html` card has an entry or a shrink-only
  baseline reason, bounds/options enforced, expect, secrets, defaults match `pages/common`.

## Findings (Opus review) and triage
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | MinTillVersion 0.36.0 on keys settable since #2289 | Fixed: empty = predates registry; `expect_min_till_version` top-level field instead |
| 2 | minor | on-token assistant ignores tiers (describe value, settings_set) | Fixed: `AssistantVisible()` gates both, unit test |
| 3 | minor | report fix-ups could overwrite a secret marker | Fixed + test |
| 4 | minor | 4 more keys ride on the heartbeat, unpinned | Intended (card: all non-secret keys); read-only set pinned by a literal test |
| 5 | minor | hand-copied defaults | Fixed: test against `common.Default*` and local bounds |
| 6 | minor | tautological tests | Strengthened (literal bounds/defaults pins); secret-report test uses synthetic secret entries |
| 7 | minor | check and write not atomic | Accepted, documented (directives run serially) |
| 8 | minor | 200-byte cut could split UTF-8; Secret+never | Fixed + tests |
| 9 | minor | receipt header/footer tier normal | Changed to sensitive (owner default relayed 2026-11-04): header/footer lines carry seller identity |
| 10 | nit | generator relative path | Fixed: refuses to run without go.mod |

## Verified beyond automated tests
Old vs new `cloudSetTillSetting` compared case by case (bounds, messages, stored spelling, audit, rederive). One
message changed: kiosk.payment_mode now "must be one of kiosk, counter". Full `go test ./...` green;
`pos.allow_negative_inventory` unchanged (pinned). No UI strings added, so no locale keys. Not driven in a real
browser (no UI change).

Verdict: safe to merge. Deferred: ut-cloud consumes the artefact (slice 1 cloud side); slice 2 regions listed in the
test baseline.
