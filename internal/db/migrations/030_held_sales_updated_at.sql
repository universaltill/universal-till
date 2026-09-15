-- 030_held_sales_updated_at.sql — ADR-0093 (universaltill/ut-docs#1920,
-- "Cross-till held-sale (open order) sync via idempotent primary-side
-- write-through"). held_sales gains updated_at: the timestamp the
-- primary's predicate-guarded upsert (HeldSalesRepo.UpsertIfNewer,
-- POST /api/sync/held-sales/upsert) compares so two tills pushing an
-- update to the SAME parked order close together resolve as
-- last-writer-wins with a clean, detectable refusal instead of a silent
-- clobber. Kept separate from created_at on purpose: created_at is the
-- FIRST-parked time the Open orders page shows as an order's age, which
-- HeldSalesRepo.Upsert deliberately leaves untouched on a re-park
-- (ut-docs#1918) -- that behaviour must survive this column.
--
-- Same "2006-01-02 15:04:05" UTC text shape as created_at (datetime('now')),
-- so the guard's `updated_at <= ?` compares correctly as plain text, the
-- same way every other created_at/updated_at column in this schema already
-- does.
--
-- SQLite cannot ADD COLUMN with a non-constant default (datetime('now')
-- is refused with "Cannot add a column with non-constant default"), so
-- the column lands with a constant '' placeholder and the UPDATE right
-- after backfills every pre-existing row from its created_at -- the
-- exact value ADR-0093 specifies for existing rows. HeldSalesRepo's
-- Insert/Upsert/UpsertIfNewer all write updated_at explicitly on every
-- write, so no row written after this migration ever carries the
-- placeholder; the `WHERE updated_at = ''` bound keeps the backfill
-- itself idempotent on replay (it can never touch a row that already has
-- a real value).
--
-- held_sales_archive gets the same column in the same migration, so the
-- Settings → Data → Clear/Restore round-trip (reset_archive_repo.go's
-- held_sales cols string, pinned by reset_test.go) carries it instead of
-- restoring every held sale back at the '' default -- the exact
-- silent-loss class 055 (held_sales_archive.table_id), 056
-- (tracking_token), 007 (local_date) and 013 (display_no) each had to fix
-- after the fact. Backfilled from created_at for the same reason.
--
-- primary_synced (ADR-0093 Amendment A): 1 once THIS till has confirmed the
-- row exists on the primary -- set by the replica write-through's
-- mirror-on-success path and by the Open orders page's merge for any id the
-- primary's list returned; a row taken purely through the local-only
-- fallback (primary unreachable) stays 0. It is the minimal marker that
-- lets "absent from the primary because it never learned of this row"
-- (keep: the legitimate outage-taken order) be told apart from "absent
-- because the primary has since resolved it" (drop: a ghost that could be
-- re-rung after the money already moved -- review finding F2). Meaningful
-- on a replica only; the primary's own rows and a standalone till's simply
-- carry 0. Constant default, so no backfill is needed: every pre-existing
-- row predates the write-through and was never confirmed on a primary.
--
-- db.go's execMigrationStatements supplies ADD COLUMN idempotence itself
-- (ut-docs#1412, checked against pragma_table_info before each statement
-- runs), so this migration is safe to replay unmodified, same as 029.
ALTER TABLE held_sales ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';
UPDATE held_sales SET updated_at = created_at WHERE updated_at = '';
ALTER TABLE held_sales_archive ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';
UPDATE held_sales_archive SET updated_at = created_at WHERE updated_at = '';
ALTER TABLE held_sales ADD COLUMN primary_synced INTEGER NOT NULL DEFAULT 0;
ALTER TABLE held_sales_archive ADD COLUMN primary_synced INTEGER NOT NULL DEFAULT 0;
