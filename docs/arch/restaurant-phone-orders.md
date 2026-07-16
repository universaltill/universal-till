# Restaurant phone orders — voice, translate, kitchen print (BACKLOG)

> Status: **backlog / vision**, not built. Captured 2026-07-16 (Farshid).
> For hospitality/restaurant users. Self-hosted AI only (see the "AI
> self-hosted" rule — no paid AI APIs).

## The idea

The till **answers the restaurant's phone**. A customer calls and orders **in
their own language**; the till transcribes and **translates to the shop's
language**, turns it into an order (line items off the menu), **saves it**, and
**prints it to the kitchen printer** — so staff who don't speak the caller's
language can still take the order.

## Flow

1. **Answer the call** — a phone number rings the till (VoIP/SIP, or a
   softphone/gateway on the LAN). The till picks up (or a "phone order" screen
   opens with live audio).
2. **Speech → text** in the caller's language (self-hosted **Whisper**-class STT
   on the shop mini-PC / homelab — same infra as the AI plugin's Ollama).
3. **Translate** the transcript to the shop language (self-hosted translation
   model). Multilingual is already core to us; this extends it to voice.
4. **Understand the order** — map spoken items to **menu items** (fuzzy match +
   the AI model), with quantities, modifiers ("no onions"), and clarifying
   questions if ambiguous. Show it on-screen for the operator to confirm.
5. **Save the order** — as a held/pending order (we already have held sales),
   tagged as a phone order with caller info + pickup/delivery.
6. **Kitchen print** — send a kitchen ticket to the **kitchen printer** (we have
   ESC/POS printing; this needs a kitchen-ticket format and per-station routing)
   — the original + translated text so the kitchen reads it in the shop language.

## How it maps to what we have / need

- **Self-hosted AI** — STT + translation + order-parsing run on the shop's own
  Ollama/Whisper box (never a paid API). The AI plugin already establishes the
  self-hosted endpoint pattern.
- **Print** — ESC/POS + auto-print exist; add a **kitchen-ticket** template and
  **printer routing** (kitchen vs receipt vs bar) — overlaps the roadmapped
  **KDS** (Kitchen Display System) and the hospitality bundle.
- **Held/pending orders** — reuse held sales as the order store; add order
  type (dine-in/takeaway/delivery/phone) + status.
- **Telephony is the new piece** — SIP/VoIP integration (a device/integration
  plugin driving a SIP gateway or softphone), plus live audio capture. This is
  the hardest, most hardware/infra-dependent part.
- **Plugin shape** — a hospitality/telephony **integration plugin** (+ the AI
  endpoints) rather than core, so non-restaurant tills don't carry it.

## Open questions

- Telephony path: SIP trunk + on-prem gateway vs a cloud number forwarded to a
  local softphone; latency of real-time STT; barge-in/half-duplex.
- Fully automated answering vs assisted (operator confirms the parsed order) —
  assisted first is safer and still a huge help.
- Menu modeling: modifiers/combos/options (needs a richer menu model than the
  current retail catalog).
- Accents/dialects and noisy-line STT accuracy.

## Sequence (when picked up)

1. Order model for hospitality (types, modifiers) + kitchen-ticket print +
   printer routing (also unlocks KDS).
2. Self-hosted STT + translation on the AI endpoint; a "phone order" assisted
   screen (paste/á live transcript → parsed order → confirm → hold → kitchen).
3. Telephony integration (SIP) — the auto-answer piece.
