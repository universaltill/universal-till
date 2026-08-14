# Vertical feature backlog: barber/salon + restaurant (2026-07-28)

Requested features checked against real code, not assumed — same
approach as `germany-pos-parity-backlog.md`. Status key: ✅ exists,
🟡 partially exists / adjacent feature exists, ❌ genuinely missing.

Both verticals reinforce a pattern already showing up in ADR-0026 (shop
type captured at setup): a barber shop and a restaurant need different
*default* till behavior out of the box, not just different menu
categories. Worth keeping in mind when that wizard step gets built —
shop type could reasonably pre-enable/suggest the relevant items below.

## Barber shop

**1. Per-staff revenue/commission attribution** ❌ — genuinely missing.
Checked for any `StaffID`/`EmployeeID`/`CommissionRate`-shaped field on
a sale or sale line: none exists. The ask: when a single payment covers
multiple customers served by different barbers (Farshid's example: him
and his son, cut by two different staff), the till needs to split that
one payment by staff member, so end-of-day/period reports show "how
much did each barber bring in." This is a **line-level** concept, not a
sale-level one — a sale needs each line individually attributable to
whoever performed that service, not just one staff tag for the whole
transaction. Real scope: a `staff_id` (or `performed_by`) field on sale
lines, a staff/roster concept if one doesn't already exist under a
different name (checked — it doesn't; `Benutzer`/users exist for
till login, but nothing distinguishes "logged in as" from "performed
this specific service"), and a report grouped by staff over a date
range.

**2. Calendar/booking/reservation** ❌ — confirmed genuinely absent.
Nothing in `internal/` implements appointments, a calendar, or
reservations of any kind. This is a substantial new feature area, not
an extension of something existing — real scope: appointment
slots/duration per service, staff availability, a booking UI (customer
-facing and/or staff-facing), and a way for a booked appointment to
become a till sale when the customer actually pays. Plugin-first
(ADR-0002) is likely right here — booking is naturally per-vertical, not
core POS.

**3. Selling retail items alongside services** ✅ — already just works.
The catalog has no service/product distinction that would block this;
a barber shop selling pomade or shampoo is ordinary item sale, no gap.

**4. Tips** ❌ — same gap already flagged in
`germany-pos-parity-backlog.md`'s SumUp research: zero tip concept
anywhere in the domain model. Relevant here too, and worth noting tips
**compound with item 1** — if tips also need per-staff attribution (the
customer tips *their* barber specifically, not the shop generally),
the tip field needs the same `staff_id` association as the commission
split above. Same underlying schema work likely covers both.

## Restaurant

**1. Table booking/reservation** ❌ — same gap as barber-shop calendar
booking, likely the same underlying feature (a booking system that's
configurable per-vertical: hair appointment slots vs. table
reservations are structurally similar — a resource, a time slot, a
duration, a party size instead of a service type).

**2. Online ordering (delivery/takeaway)** ❌ — genuinely absent, and
importantly **not the same thing as the existing self-order kiosk**
(ADR-0020). Checked: that kiosk is a fixed, in-store, network-attached
device — offline-first by design, tied to the shop's own LAN. "Online
ordering" as asked here means a customer ordering remotely from their
own phone/home, which is a different shape of feature (needs to work
when the shop's till is briefly offline, needs delivery address/ETA
concepts the kiosk has no reason to have). Don't conflate these two in
planning even though they share a catalog/checkout core.

**3. Third-party delivery app integration (Deliveroo etc.), printing to
the kitchen printer** 🟡 — **the taxonomy already has a slot for this**:
`delivery` is one of the 20 canonical plugin types (ADR-0002,
`internal/plugins/types.go`'s `PluginTypeDelivery`), but no delivery
plugin has been built yet — checked the plugin repo list, none exists.
Kitchen ticket printing itself already exists and already has an
`OrderType` concept (`internal/print/kitchen.go`: "dine-in / takeaway /
delivery / phone") and a free-text `Table` label field — so the
printing half is ready; what's missing is the actual **inbound**
integration (receiving an order from Deliveroo/Uber Eats/Just Eat's
API and turning it into a till sale that then prints). This is a real,
scoped `ut-plugin-delivery-{provider}` per platform.

**4. Order for collection** 🟡 — `OrderType` already includes a concept
adjacent to this (dine-in/takeaway/delivery/phone), but "collection"
specifically (customer orders ahead, arrives later to pick up, no
delivery) isn't one of the four listed values — likely just adding a
value to that enum plus whatever UI flags an order as "awaiting
pickup," not a new subsystem.

**5. Order at the table** ❌ — related to item 2 (online ordering) but
distinct: a QR-per-table flow where the customer's own phone opens a
menu scoped to *their* table specifically. Checked the self-order
kiosk code for any table concept: none — it's single-location, no
per-table context anywhere. This would need the kitchen ticket's
existing free-text `Table` label to become a real identifier the
ordering flow can address (`/order?table=12`), not just a print label.

**6. Tips and service charge** ❌ — tips: same gap as everywhere else in
this doc. **Service charge is a distinct, additional gap**, not a
synonym for tips — checked specifically, zero matches for
`service_charge`/`ServiceCharge` anywhere. Restaurants often need both
simultaneously and they behave differently (service charge is usually
a till-set percentage automatically added, sometimes mandatory for
large parties; a tip is customer-discretionary) — worth modeling as two
separate fields from the start rather than one that gets awkwardly
overloaded later.

## Cross-cutting observations

- **Tips, service charge, and per-staff commission attribution are one
  connected piece of schema work**, not three separate features — a
  sale line needs to optionally carry: who performed/sold it, a tip
  amount, and a service-charge amount, all independent of each other.
  Worth designing that shape once rather than bolting each on
  separately as it comes up (this is now the third time tips alone has
  surfaced this session — SumUp comparison, barber shop, restaurant).
- **Booking/reservation is one feature serving two verticals**, not two
  separate builds — a barber's appointment slot and a restaurant's
  table reservation are the same underlying resource-time-slot model
  with different labels. Worth scoping as a single plugin (or core
  feature, given how central it is to two of the most common shop
  types) rather than duplicating logic per vertical.
- **Delivery integrations are additive to what exists** (the plugin
  type, the `OrderType` enum, kitchen printing) — this is the
  cheapest of the gaps here to close per-provider, since the taxonomy
  and printing plumbing are already there.
