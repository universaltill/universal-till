-- Live-basket table claims (universaltill/ut-docs#1390). Table occupancy
-- used to have exactly one durable source: held_sales.table_id (054). But
-- the LIVE, not-yet-held basket's table (pos.Service.tableID, set by
-- POST /api/pos/table) was pure in-memory state — nothing persisted a
-- claim at the instant a table was picked, so IsTableFree /
-- ListTablesWithState could not see it, and a second basket (the next
-- order on the same till, or a parked order being moved) could take the
-- same table with no rejection.
--
-- One row per CURRENTLY-claimed table, written the moment the live basket
-- picks it and deleted when the basket clears the table, is parked (the
-- held_sales row then carries the occupancy), tenders, or is reset. A
-- resumed held sale re-claims its table before its held_sales row is
-- deleted, so occupancy moves back to a live claim with no falsely-free
-- window. Exactly one occupancy source per lifecycle stage, by design.
--
-- Deliberately NO owner/session column: there is exactly one live basket
-- per till process, so the handler already knows whether a pick is "mine"
-- (re-picking the basket's own current table is short-circuited before
-- any DB call) — an owner column would model a distinction nothing here
-- can observe. The PRIMARY KEY is the whole reservation mechanism: a
-- claim is INSERT OR IGNORE, and RowsAffected == 0 means "already
-- occupied", race-free, with no check-then-insert window.
-- REFERENCES tables(id): a claim can only ever point at a real table.
-- Timestamp is RFC3339 TEXT like every other table here; it feeds the
-- floor plan's "occupied since" minutes exactly like held_sales.created_at.
-- IF NOT EXISTS, same as 076's indexes: the migration-replay tests in
-- internal/db rewind schema_migrations to an earlier version and re-run
-- every later migration against an already-migrated database, so DDL
-- added here must be re-runnable rather than needing its own per-test
-- rewind helper.
CREATE TABLE IF NOT EXISTS table_claims (
    table_id   TEXT PRIMARY KEY REFERENCES tables(id),
    claimed_at TEXT NOT NULL
);
