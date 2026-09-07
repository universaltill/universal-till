-- ADR-0081 (ut-docs#1587): internal/fiscal's settings keys drop ADR-0048's
-- Germany-specific "TSE" vocabulary for market-neutral "signing device" —
-- the gate already covers Turkey (YN ÖKC, ut-docs#1208) and every further
-- ADR-0047 market inherits the name. Policy is unchanged (ADR-0048
-- Decisions 1-4 stand); only the key names move, so a till gated or
-- configured before this upgrade reads the identical value under the new
-- key immediately after, with no operator action and no gap where the gate
-- forgets a shop's prior configuration.
--
-- Each UPDATE is a plain in-place row rename, and on a single till at most
-- one row can ever match each WHERE: a fresh install (or a re-run against an
-- already-migrated database) has no row under the old name, so every
-- statement is a no-op there.
-- fiscal.system_of_record is deliberately absent — it was never TSE-named.
--
-- The paired DELETE before each UPDATE exists for MULTI-TILL, not for the
-- single-till upgrade (independent review, 2026-09-07). None of these keys
-- is in data.PerTillSettingPrefixes, so they are shop-wide: a primary dumps
-- them into the admin bundle and every joined till upserts them — and
-- settings rows are deliberately NEVER pruned on a replica
-- (sync_admin_repo.go's Phase-1 skip, precisely so a key one till knows and
-- the other doesn't survives a mid-rollout version skew). So the ordinary
-- staggered upgrade of a multi-till shop reaches a state this migration
-- would otherwise die on:
--
--   1. primary upgrades; its row is renamed to fiscal.signing_device_*
--   2. the still-old replica syncs, upserts the NEW key, and keeps its own
--      OLD key (never pruned) — the replica now holds both
--   3. the replica upgrades; a bare UPDATE would hit
--      "UNIQUE constraint failed: settings.key", and because a migration
--      runs inside a transaction that is not partial corruption but a
--      permanent, self-repeating boot failure: the ledger row is never
--      written, so every subsequent start fails identically and the till
--      cannot sell until someone edits the database by hand.
--
-- When both rows exist the NEW-named one wins and the stale old-named row is
-- dropped: the new-named row is the value that arrived from the primary,
-- which is the source of truth for a shop-wide setting, whereas the
-- old-named row is this till's own pre-upgrade copy of the same setting.
-- (In practice they hold the same value; this only decides the tie.) Leaving
-- the old row behind instead would be harmless to the gate but would leave a
-- dead fiscal.tse_* row on disk for the next reader to trip over.
--
-- Both statements stay idempotent and no-op on a fresh install: the DELETE's
-- EXISTS is false when the new key is absent, and false again on a re-run
-- once the old key is gone.

DELETE FROM settings WHERE key = 'fiscal.tse_configured'
  AND EXISTS (SELECT 1 FROM settings s WHERE s.key = 'fiscal.signing_device_configured');
UPDATE settings SET key = 'fiscal.signing_device_configured'    WHERE key = 'fiscal.tse_configured';

DELETE FROM settings WHERE key = 'fiscal.tse_failing_since'
  AND EXISTS (SELECT 1 FROM settings s WHERE s.key = 'fiscal.signing_device_failing_since');
UPDATE settings SET key = 'fiscal.signing_device_failing_since' WHERE key = 'fiscal.tse_failing_since';

DELETE FROM settings WHERE key = 'fiscal.tse_override_until'
  AND EXISTS (SELECT 1 FROM settings s WHERE s.key = 'fiscal.signing_override_until');
UPDATE settings SET key = 'fiscal.signing_override_until'       WHERE key = 'fiscal.tse_override_until';

DELETE FROM settings WHERE key = 'fiscal.tse_override_reason'
  AND EXISTS (SELECT 1 FROM settings s WHERE s.key = 'fiscal.signing_override_reason');
UPDATE settings SET key = 'fiscal.signing_override_reason'      WHERE key = 'fiscal.tse_override_reason';

DELETE FROM settings WHERE key = 'fiscal.tse_override_actor'
  AND EXISTS (SELECT 1 FROM settings s WHERE s.key = 'fiscal.signing_override_actor');
UPDATE settings SET key = 'fiscal.signing_override_actor'       WHERE key = 'fiscal.tse_override_actor';
