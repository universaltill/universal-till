-- ADR-0083 (ut-docs#1767): internal/fiscal's two signing-device POSTURE keys
-- become per-country rows. Until now Germany's TSE gate and Turkey's YN ÖKC
-- gate read and wrote ONE shared row each for "configured" and
-- "failing since", and the row a market's gate read was selected by
-- store.country — ordinary, manager-writable shop config. ut-docs#1750
-- found the seam that opens: country=TR → a cashier sale auto-confirms the
-- ÖKC → country=DE left a German till reading fiscal.Allowed with no TSE
-- at all. From this version each market has its own row,
--   fiscal.signing_device_configured.<cc>    e.g. ...configured.de / .tr
--   fiscal.signing_device_failing_since.<cc>
-- with <cc> the lower-cased, trimmed store.country — exactly what
-- fiscal.SigningDeviceConfiguredKey / SigningDeviceFailingSinceKey compute
-- in Go, so the row this migration writes is the row the gate reads. Gate
-- policy is unchanged (ADR-0048 Decisions 1-4 stand; ADR-0081's naming
-- stands): only the row's scope moves, so a shop configured before this
-- upgrade reads the identical posture under its own country immediately
-- after, with no operator action and no gap where the gate forgets it.
--
-- fiscal.system_of_record and the three fiscal.signing_override_* keys are
-- deliberately absent — ADR-0083 Decision 2 keeps them global.
--
-- Same DELETE-then-UPDATE shape as 009, for the same reason: none of these
-- keys is in data.PerTillSettingPrefixes, so they are shop-wide and
-- replicate, and settings rows are never pruned on a replica
-- (sync_admin_repo.go's Phase-1 skip). A staggered multi-till upgrade
-- therefore reaches a state a bare UPDATE would die on:
--
--   1. primary upgrades; its row is renamed to ...configured.<cc>
--   2. the still-old replica syncs, upserts the NEW per-country key, and
--      keeps its own OLD flat key (never pruned) — the replica holds both
--   3. the replica upgrades; a bare UPDATE would hit
--      "UNIQUE constraint failed: settings.key" inside the migration's
--      transaction — a permanent, self-repeating boot failure, the till
--      cannot sell until someone edits the database by hand.
--
-- When both rows exist the per-country one wins and the stale flat row is
-- dropped: the per-country row is the value that arrived from the primary,
-- the source of truth for a shop-wide setting; the flat row is this till's
-- own pre-upgrade copy of the same setting.
--
-- The target name is computed from the till's OWN store.country row, which
-- is itself shop-wide and synced, so it reflects the shop's actual,
-- currently-declared market at migration time. A till holding a flat
-- posture row but NO store.country row is not a reachable state — the gate
-- (fiscal.RequiresHardGate) needs a declared country before either key
-- could ever have been set true — so that edge is left as a no-op rather
-- than guessed at: the `AND EXISTS (... 'store.country' ...)` guard on each
-- UPDATE keeps the flat row untouched. The guard is also load-bearing for
-- correctness, not just intent: settings.key is a plain TEXT PRIMARY KEY,
-- which SQLite permits to be NULL, so without it a missing store.country
-- would turn the scalar subquery into NULL and the UPDATE would silently
-- rewrite the row's key to NULL. An empty/whitespace-only store.country is
-- treated the same as a missing one.
--
-- Every statement is idempotent and a no-op on a fresh install: no flat row
-- exists, so each DELETE's WHERE and each UPDATE's WHERE match nothing; on a
-- re-run after the rename the flat row is already gone.

DELETE FROM settings WHERE key = 'fiscal.signing_device_configured'
  AND EXISTS (SELECT 1 FROM settings s
              WHERE s.key = 'fiscal.signing_device_configured.' ||
                    (SELECT lower(trim(c.value)) FROM settings c WHERE c.key = 'store.country'));
UPDATE settings
   SET key = 'fiscal.signing_device_configured.' ||
             (SELECT lower(trim(c.value)) FROM settings c WHERE c.key = 'store.country')
 WHERE key = 'fiscal.signing_device_configured'
   AND EXISTS (SELECT 1 FROM settings c WHERE c.key = 'store.country' AND trim(c.value) <> '');

DELETE FROM settings WHERE key = 'fiscal.signing_device_failing_since'
  AND EXISTS (SELECT 1 FROM settings s
              WHERE s.key = 'fiscal.signing_device_failing_since.' ||
                    (SELECT lower(trim(c.value)) FROM settings c WHERE c.key = 'store.country'));
UPDATE settings
   SET key = 'fiscal.signing_device_failing_since.' ||
             (SELECT lower(trim(c.value)) FROM settings c WHERE c.key = 'store.country')
 WHERE key = 'fiscal.signing_device_failing_since'
   AND EXISTS (SELECT 1 FROM settings c WHERE c.key = 'store.country' AND trim(c.value) <> '');
