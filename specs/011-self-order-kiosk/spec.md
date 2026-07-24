# 011 — Self-order kiosk + item modifiers

Decision record: `ut-docs/adr/0020-self-order-kiosk-and-item-modifiers.md`
(read that first — this doc is the implementation breakdown, not the "why").

## Goal
A till installable as a self-order kiosk: customer browses/searches the
catalog, customizes an item (modifiers), pays by card, order reaches
whoever fulfills it. Connected to the shop's regular till and back-office
via the existing multi-till sync (ADR-0011) — no new sync mechanism.

## Phases (build and ship in this order — each is independently useful)

### Phase 1 — Item modifiers (catalog data + cashier POS UI)
Foundational: both the existing cashier till and the future kiosk need
this. Ships value on its own (cashier can already ring up "flat white,
extra shot" correctly) before the kiosk exists.

- Migration (append-only): `item_modifier_groups` (id, item_id, name,
  required, min_select, max_select, sort_order), `item_modifier_options`
  (id, group_id, name, price_delta_minor, sort_order, is_active).
- Sale line items: a snapshot of chosen options (name + price_delta at
  time of sale — never a live FK to the option row, so a later catalog
  edit can't rewrite history), stored as its own table
  (`sale_line_modifiers` or similar) keyed to the sale line.
- `ItemRepo`/`POSRepo` methods: list modifier groups+options for an item,
  compute a line's total (base price + sum of chosen deltas).
- Cashier POS UI: when an item with modifier groups is tapped, show a
  selection step (respecting required/min/max) before it lands in the
  cart; receipt shows the chosen modifiers as sub-lines.
- Admin catalog UI: manage groups/options per item (add/edit/reorder/
  deactivate).

### Phase 2 — Self-order kiosk till mode (shell)
- `display.mode` gains `self_order`. Settings UI: new option in the
  existing mode selector (`web/ui/pages/settings.html`), manager-gated
  like the existing `backoffice` option.
- New route(s) for the self-order flow, locked behind the mode: a
  self-order-mode till landing on `/` gets the kiosk flow, not the cashier
  screen or a 403 — same role-aware-fallthrough pattern the backoffice-mode
  review already established (audit every existing `mode == "backoffice"`
  branch for a kiosk-mode gap while touching this).
- No admin/settings surface reachable from the kiosk UI itself.

**Auth model decision (found while building this phase, not anticipated
when the spec was first written):** every route in this app requires a
cashier PIN session today (`docs/architecture/pos-auth.md` — the auth
middleware gates everything except `/login`, `/api/auth/*`, `/public/*`,
`/themes/*`, `/plugin-icons/*`, `/healthz`). A self-order kiosk is used by
anonymous walk-up customers who cannot be expected to enter a cashier PIN.
Two options considered:
1. Auto-create/resolve a session for a dedicated seeded "kiosk" operator
   whenever a self-order-mode till's landing route is hit with no valid
   session — mimics real cashier attribution but adds new session-
   auto-creation machinery and session-expiry edge cases for a kiosk that
   sits open all day with no one "logging in."
2. **Chosen**: exempt the self-order routes from the auth middleware
   (`internal/auth/middleware.go`'s `exempt()`, same precedent as the
   `/themes/`/`/plugin-icons/` static-asset exemptions), and attribute
   kiosk sales to a fixed, well-known seeded "kiosk" user id at sale-
   completion time — not derived from a session at all, since there isn't
   one. Simpler, no new session lifecycle to reason about. A manager
   needing admin access from a kiosk-mode till still goes through the
   normal, fully-gated `/login` flow — only the self-order surface itself
   is exempt, nothing else.
- Idle timeout → reset to the start screen. **Correction found while
  building**: `auth.idle_lock_minutes` is fundamentally session-based
  (revokes a cashier's session server-side) — since the self-order route is
  auth-exempt (no session at all), that mechanism doesn't apply. Uses a
  separate, simpler setting instead: `kiosk.idle_reset_seconds`
  (manager-gated, default 60s) driving a lightweight client-side-only
  timer that reloads the self-order landing page after inactivity — no
  server-side session to revoke, nothing to lose yet at the Phase 2 shell
  stage (no cart exists until Phase 3). Phase 3/4 will need to revisit this
  once there's real in-progress cart state to discard on reset.

### Phase 3 — Browse, search, customize, cart
- Catalog browse (category chips + item grid, reusing existing catalog
  data) sized for a customer-facing touchscreen, not the cashier's dense
  button grid.
- A real search box — doesn't exist anywhere in the sale flow today (only
  the admin catalog editor has one). This is also the eventual target for
  the keyboard-layout-plugin transliteration option, if/when that's built
  (separate item, not blocking this phase).
- Tapping an item with modifier groups shows the Phase 1 customization
  step before add-to-cart; cart shows line + chosen modifiers + running
  total.

### Phase 4 — Checkout
- Card/contactless payment only for v1 (existing payment plugin
  taxonomy — no new extension point). No cash drawer interaction.
- Order confirmation screen; receipt (print or on-screen, shop-configurable
  like existing receipt settings).

### Phase 5 — Order handoff / kitchen ticket
- Shared mechanism with the phone/table-QR ordering backlog item (not
  built twice). Scope this phase once Phase 1-4 are live and the shape of
  a "kiosk order" is concrete — premature to design the ticket format
  before real orders exist to look at.

## Explicitly out of scope for v1
- Cash acceptance at the kiosk (card/contactless only; cash-handling
  hardware plugin is a later addition if a real shop needs it).
- Negative/substitution modifiers ("no cheese" style) — price deltas are
  additive-only for v1.
- OS-level kiosk packaging (chromium --kiosk boot cage) — tracked
  separately in QUEUE.md ("Pi kiosk" boot test), orthogonal to this spec.

## Open questions to revisit once Phase 1-2 are live
- Does a kiosk order need its own receipt-numbering prefix (like replica
  tills already get, ADR-0011) for reporting clarity?
- Multi-language: does the kiosk need an explicit language picker at the
  start screen, or does it inherit the till's configured locale?
