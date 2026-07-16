# Booking / appointments — service businesses (BACKLOG)

> Status: **backlog / vision**, not built. Captured 2026-07-16 (Farshid).
> For appointment-based shops: barbers, salons, dentists, clinics, etc.
> Plugin-first: the booking engine + each channel integration is a plugin.

## The idea

Turn the till into a booking system for service businesses: customers book
appointments, staff/resources are scheduled, and reminders + confirmations go
out over the customer's channel (**calendar, WhatsApp, SMS**), with an
**automatic booking** flow (self-service online + auto-confirm/reschedule).

## Pieces

1. **Booking engine** (core-ish seam or a plugin): services (duration, price),
   staff/resources, availability/calendar, appointments with status
   (requested/confirmed/done/no-show), linked to a sale at checkout.
2. **Channel integration plugins** (integration type, one per provider):
   - **Calendar** — Google Calendar / Outlook / CalDAV (two-way sync, free/busy).
   - **WhatsApp** — WhatsApp Business API (confirmations, reminders, reschedule
     links). Provider e.g. Meta Cloud API / Twilio / 360dialog.
   - **SMS** — Twilio / local gateways (Turkey: İleti Merkezi/Netgsm; UAE/UK
     providers).
3. **Automatic booking** — an online self-service page (book a slot → auto-hold
   → confirm), reminders on a schedule (scheduler plugin), auto no-show handling.

## How it maps to us

- **Plugin-shaped** (per the rule): booking channels = integration plugins on
  the connector pattern (settings for API keys, `net:*`, offline queue,
  scheduler for reminders). Same shape as ERP/notification connectors.
- **Reuses**: held sales / order model, the AI + multilingual stack (reminders
  in the customer's language), and the payment seam (deposits/prepay).
- **Demo/sandbox first** (per Farshid): build against provider **sandboxes**
  (Twilio trial, Meta WhatsApp test number, Google Calendar API test project)
  before a real customer — no customer data needed.

## Sequence (when picked up)

1. Service + resource + appointment model; a booking/calendar UI.
2. Reminder scheduler + a generic notification connector (SMS/WhatsApp).
3. Calendar two-way sync plugin.
4. Public self-service online booking page + auto-confirm.

Related: restaurant-phone-orders.md (shares notification + AI), device-plugin-
suite.md, [[plugin-first]].
