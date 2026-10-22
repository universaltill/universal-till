# Review — setup wizard skips the shop-type step when the paired shop already has one (ut-docs#4830, ADR-0171)

**Date:** 2026-10-22 · **Built by:** Sonnet 5.5 · **Independent review:** Opus 5.5 (different model); fixes by the orchestrator.

## What shipped
- `enroll.PairProfile` keeps the pair response's `shop_type` (enum only; unknown/wrong case/missing = "").
- Cloud-path wizard: step 5 is not rendered when the held pairing has a valid type; "Your shop" shows it read-only (`setup.cloud.shop_type`); empty/unknown still asks.
- Server decides: `applyCloudShop` replaces any posted `shop_type` (form and query) with the cloud's; one shared `saveSetupShopType` for picked and cloud types (layout sync, order-type default, plugin reload).
- Leaf package `internal/shoptype` holds the list (import cycle with plugins); `plugins.ShopTypes` / `IsKnownShopType` delegate to it.
- Help `multitill.md` in en/de/tr/fa/ar; docs-shots surface hash and the four multitill topic hashes refreshed (no screenshotted page changes).

## Findings (Opus)
| # | Finding | Handling |
|---|---|---|
| B | `guard-docs-shots` failed: multitill topic hashes stale | Fixed (hashes = sha256 of the files; guard passes) |
| 1 | Page's skip rule untested | Fixed: assertion on the exact rule text; no e2e exists for the cloud path |
| 2 | tr label "Dükkan türü" off-terminology | Fixed: "İşletme türü" |
| 3 | Help says "when you chose one on my." (a till-reported type also counts) | Accepted, minor |
| 4 | Language packs need `setup.cloud.shop_type` (de, es, pt) | Follow-up PRs right after merge (lang-pack-drift blocks main until then) |

## Verified
Crafted/foreign posts cannot override the cloud's type; non-cloud path unchanged; side effects run once; no second copy of the list; offline-first (no new network calls); mutation checks (override removed, enum check removed: tests fail).

## Not verified
A real browser run of the Alpine navigation (rendered-HTML assertions only).

## Safe to merge
Yes, with the three language-pack PRs following.
