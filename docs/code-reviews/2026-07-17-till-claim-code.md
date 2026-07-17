# Code review — "Claim this store" on the till (ADR-0013 layer 2)

**Date:** 2026-07-17
**Branch:** `feat/till-claim-code`
**Companion to:** ut-market-place `2026-07-17-store-claim-flow.md`.

## What changed

- `enroll.ClaimCode(ctx, cfg)`: POSTs `/v1/stores/claim-code` with the
  store token, returns the code + absolute claim URL (marketplace web base
  derived the same way as the signing-key fetch: endpoint minus `/api`).
- Settings → Till registration (registered state, manager-only): a
  "Claim this store" section — button → `POST /api/enrol/claim-code` →
  big monospace code + validity note + link to the marketplace claim page
  (`/ui/claim?store=…`, store id pre-filled). Always answers 200 with a
  swappable message (the enrol-button lesson: HTMX drops non-2xx).
- i18n ×4 (620 keys), `.claim-code` CSS (large, letter-spaced — the code
  is read off the till screen).

## Flow end-to-end

Till (manager) → code on screen → owner opens the claim link on any
device → signs in with Universal Till ID (OIDC; canonical-host fix from
this morning applies) → enters the code → store owned; "My stores" shows
devices + entitlements.

## Verification

- Full suite + i18n guard green; enroll/pages tests pass.
- Prod: claim pages verified deployed (guard redirects anonymous browsers
  to login); code issuance exercised against prod with a scratch store.
  Full redemption needs a browser sign-in (admin has U2F) — Farshid's
  first real claim is the field test.
