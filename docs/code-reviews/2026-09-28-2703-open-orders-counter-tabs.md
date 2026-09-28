# Review: Open orders tabs (On hold / Pay at the counter), legacy counter orders payable, old page removed (ut-docs#2703 reopened)

**Date:** 2026-09-28 · **Lane:** lane:local · **Built by:** Claude Opus 5.5 (subagents) · **Reviewed by:** Claude Fable 5.1 ×2 (independent subagents)

## Why reopened
Owner: the "Pay at counter" page showed an order with only **Mark collected** (closed unpaid) and "open orders need 2 tabs ... both act the same: the order opens on the sell screen and the cashier can take payment or change it"; then "remove the page pay at the counter and all the leftover from it". The first #2703 made new counter orders held sales but dropped the two-section design and kept the legacy page.

## What shipped
- **Two tabs** on /open-orders and the sale-screen popup: *On hold* / *Pay at the counter* (split by the C-number in the held sale's snapshot, not the label), counts on each, `?tab=counter`; tapping any row resumes it onto the sell screen (normal tender → signed sale). WAI-ARIA tab keyboard handling in the popup (RTL-aware).
- **Legacy `open` kiosk rows** listed in the counter tab; tap → `ConvertOpenToHeld` (one tx: open→held + held sale under the same id) priced from the catalog (exact name match, current effective price incl. price history, modifier deltas; ambiguous names/modifiers, unmet modifier-group rules, qty ≤ 0 → left for the cashier). Unmatched lines ride in the held sale's snapshot (`add_by_hand`) and show as a notice in the basket on every resume until paid, cleared or dismissed (`POST /api/pos/add-by-hand/dismiss`); never printed, signed or in the paid sale.
- **Removed:** /kiosk-counter-orders page, templates, menu tile, demo routes, middleware poll exemption, `MarkCollected` + collected constant, 12 locale keys, help topic ×5 + screenshots. The `kiosk_counter_orders` table stays (C-numbers, links, historical rows).
- Age reads min/h/d; phone-width rows wrap (order on the first line).

## Findings
Round 1: (1) Medium: unmatched lines only in the redirect URL, lost if the resume failed or the order was reopened → **fixed** (snapshot field, every resume). (2) Low/Med: ambiguous modifier / unmet required group matched → **fixed**. (3) Low: qty ≤ 0 forced to 1 → **fixed**. (4) Low: mixed legacy order priced dine-in → **documented** in help ×5. (5) Low: test-only constant → **removed**. (6) Low: popup tab lost on re-render, no arrow keys → **fixed**.
Round 2: no defects; two nits (dead `AgeMinutes` field, an over-broad two-till comment) → **fixed** by the orchestrator (no pixel change; docs-shots surface hash refreshed).
Reviewer-confirmed: conversion race → exactly one held sale; no double tender; notice text auto-escaped; `add_by_hand` never reaches receipt/fiscal/sync decoders strictly; arrow-key handler scoped (barcode wedge unaffected).

## Verified
TDD: each new Go/e2e test failed first (tab split, conversion, idempotency/race, matcher, snapshot survival across a forced resume failure, popup tab, age text, removed routes). Full `go test ./...`, build, vet; guards data-access, i18n, help-topics, help-drift, docs-shots; e2e counter-tabs, kiosk-counter-order-held, row-geometry + 11 popup specs green. Screenshots looked at: en + fa at 1280×800, 1024×600, 360×740 (page, popup, sale notice). NOT checked: German labels on screen (pack not in e2e), dark theme, the real tablet (after release).

**Verdict:** safe to merge. Packs: de/es PRs (8 new keys, 12 removed) land right after core.
