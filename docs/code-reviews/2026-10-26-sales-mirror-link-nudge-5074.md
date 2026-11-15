# Review: the sales copy's fast path over the main-till link (ut-docs#5074 M4)

Part of ut-docs#5074 (design §5.2–§5.4, ADR-0114 amendment 2026-10-26).
Stacked on M7.

## What shipped — and the deviation

- A new `sync` nudge scope `sales_mirror` (`internal/fleetlink`). Like every
  scope it carries no data; an older till ignores it (an unknown scope owns
  no bit; a frame with known scopes beside it keeps them).
- Main till: `StartSalesMirrorNudge` looks at the change-log head once a
  second while a peer is linked (every 10 s otherwise), reads the settings
  only when the head moved, and nudges every linked till; it is also poked
  at once after the journal applies another till's sales
  (`Deps.PokeSalesMirrorNudge` in `POST /api/sync/sales`) — never from this
  till's own checkout. The head is seeded at start, so a sale before the
  first look is not missed.
- Additional till: the nudge, and the link coming up, kick the copy's pull
  (`Deps.RequestSalesMirrorPull`). A satellite asks once, is refused, and
  stays quiet through nudges for 5 minutes.
- **Deviation from the design:** no record-carrying `sales_mirror` /
  `mirror_ack` frames and no per-peer pump. The records still travel only
  over the HTTP feed, the authority; a lost, duplicated or reordered nudge
  can only delay a pull. This keeps ADR-0114 §1 ("bulk data on HTTP") and
  the link's 64-message queue out of the copy's correctness. Expected
  latency A sells → B holds it: journal push (ms) → main apply → poke →
  nudge → B's feed GET + apply: well under a second on a LAN (the reviewer
  estimated ≈ 0.1–1.4 s, mean ≈ 0.7 s, before the poke was added; with it,
  the 1 s poll no longer dominates for journaled sales). The frames remain
  specified in the contract as a later minor version.

## Off = inert

No linked peer (every single-till shop): the watcher touches nothing but
`Hub.Len()`. Peers but the copy off: the settings re-read once every 10 s,
nothing else. On: one `MAX(seq)` read a second. Nothing on the sale path.
Full suite green.

## Tests

Once per moved head, coalesced; nothing with no peer or the copy off; a sale
before the first look counts; poke ticks at once; the loop stops on cancel;
every scope owns its own bit and round-trips; the nudge kicks the copy pull
and not the admin pull; a satellite sends one request through five kicks;
kick coalescing.

## Independent review (Fable, strict)

Verdict: safe to merge dark; the deviation is acceptable as the fast path's
v1 and must be stated plainly to the owner (a signal over the link, the
record over HTTP).

1. MAJOR — first look only learned the head (a sale between link-up and the
   first tick waited for the 5-minute floor): **fixed** (seeded at start;
   not reset when off).
2. MAJOR (latency) — no post-commit poke: **fixed** for journaled sales
   (the poke from the journal apply); the main till's own sales keep the
   1 s look, so the checkout path gains nothing.
3. MINOR — satellites answered every nudge with a refused request:
   **fixed** (quiet for the linked floor after a `satellite` answer).
4. MINOR — a test would pass with the scope missing from `allScopes`:
   **fixed** (bit/round-trip test over every scope; kick-routing test).
5. MINOR — silent head-read errors: **fixed** (Warn once a minute).
6. NIT — stale "frame apply" comments: **fixed**.
7. NIT — per-second cost: **fixed** differently — the gate is cached for
   10 s, so off costs settings reads every 10 s and on costs one head read a
   second (reading the head first would have added a read a second while off).

## Deferred (card)

Record-carrying frames per the contract's reserved `sales_mirror/1`
feature: `hello.features` + cursors, a per-peer pump with drop-to-pull,
`mirror_ack`, a role filter in the hub.
