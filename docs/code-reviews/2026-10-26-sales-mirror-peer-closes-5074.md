# Review: additional tills' own closes in the sales copy, shipped dark (ut-docs#5074 M7)

Part of ut-docs#5074 (design §3.2, D9). Stacked on M3.

## What shipped

- Migration `094_peer_closes.sql` (pinned): `peer_closes` (the additional
  tills' own day closes the main till relays — never its `report_archive`,
  Z chain or numbers) and a change-log trigger gated exactly like 093's.
- `POST /api/sync/closes` (main till): pinned TLS, bearer, satellites `403`,
  `409` while off, ≤ 20 closes / 4 MiB, each validated (bounded ids,
  `created_at` in `report_archive`'s own form, JSON content); idempotent;
  a close whose id is in the main till's own `report_archive` (inherited by
  the pusher at join) is never stored as the pusher's.
- `syncPushCloses` in the journal push tick (additional till): its own
  closes past `sync.close_push_cursor` (seeded at join, in
  `report_archive.created_at`'s text form), 20 at a time; malformed rows
  skipped with a warning (never wedging), satellites send nothing; a no-op
  while the copy is off.
- Feed and state re-sync relay them (state order: sales → the main till's
  closes → the other tills' closes), never back to their own till; a
  missing one is `gone`.
- Classification, isolation guard (`peer_closes` added), demo-denied,
  session-exempt (exact path), plan pinned.

## Off = inert

The push step returns after its settings reads; the route answers `409`;
the trigger writes nothing (`TestMigration094_PeerCloses`); nothing on the
sale path (the push tick is its own loop). Full suite green.

## Tests

Relay to every other till via feed and via state (three tills); idempotent
push; echoed main close refused with and without a cursor; malformed row
skipped; route validation (21 closes, bad JSON, bad date, padded id, not an
array); satellite refused and silent; state paging of peer closes across
page boundaries; gone; join cursor format.

## Independent review (Fable, strict)

Verdict: safe to merge dark, no blockers or majors.

1. MINOR — push wedged on 400/403, logging every 30 s (satellite; a
   malformed row): **fixed** (satellites skip; 403/409 quiet; malformed rows
   skipped client-side with a warning, cursor advances).
2. MINOR — feed dedupe key ignored the close's origin: **fixed**.
3. MINOR — pushed fields trusted beyond JSON validity: **fixed** (bounded,
   trimmed ids; `created_at` format).
4. MINOR — after a main-till restore that lacks a close it already handed
   out, a till joined before this release (no cursor) could push it as its
   own: **accepted and documented** (`InsertPeerCloses`); every later join
   seeds the cursor.
5. MINOR — test gaps: **added** (see Tests).
6. NIT — `OwnClosesAfter` scans `report_archive` (≈ 1 row a day): accepted.
7. NIT — swallowed read error: **fixed** (Warn).
8. Design follow-up for M6: a promoted till holds other tills' closes only
   in `mirror_closes`, not `peer_closes`, so it cannot relay them to a till
   that joins later; and a re-joining till's own closes since the old main
   died are not pushed before the snapshot. To be handled in M6.

Contract additions (the closes route, close `id` = the origin till's
`report_archive.id`, state order, change-log entries for pushed closes) are
in the contract PR.
