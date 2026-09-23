-- 037_yuzde_usulu_pool_collections.sql — universaltill/ut-docs#988
-- ("ADR-0063 step 2/2"): the Turkey collection-side record ADR-0063
-- Decision 3 explicitly DEFERRED ("this ADR does not invent the
-- collection-side mechanism ... that is #965's own remaining scope").
--
-- What this table is: an operator-entered record that "we collected X
-- today under yüzde usulü" (İş Kanunu 4857 art. 51). It is deliberately
-- NOT a customer-facing bill line — a Turkey service-charge line on the
-- bill is forbidden outright (ut-docs#962, common.ServiceChargeForbidden),
-- so there is no sale_charges/payments row a pool collection could be
-- derived from. That is exactly why an independent table is needed: until
-- this migration, worker_allocations was its own only evidence that a
-- pool existed, so WorkerAllocationsSummary's "received" for
-- 'yuzde_usulu_pool' summed the very distribution rows it was supposed to
-- be checked against — a tautology. With this table, "received" (what was
-- collected into the pool) and "allocated" (what has been distributed out
-- of it, from worker_allocations) are two independent records that can
-- legitimately differ.
--
-- This table is inert — zero rows — for any till that never uses yüzde
-- usulü, exactly like sale_charges is empty for a till with one ordinary
-- service charge (ADR-0063 Decision 4). Nothing here decides WHETHER or
-- WHEN a shop operates under yüzde usulü, or at what percentage; that
-- stays country-policy/plugin-shaped (ADR-0050, ADR-0009).
--
-- amount_minor is money (internal/money.Money) stored as a raw minor-unit
-- integer at this DB boundary, the same convention as
-- worker_allocations.amount_minor and every other monetary column.
--
-- basis_note is free text describing how the pool is meant to be split
-- ("kitchen 30% / floor 70%") — the collection-side twin of
-- worker_allocations.note, and, like it, purely informational: nothing
-- parses or enforces it.
--
-- recorded_by is the manager's user id who entered the collection. Like
-- worker_allocations.cashier_id (and audit_log.actor_id) it carries NO
-- FOREIGN KEY to users(id): this is an append-only historical record that
-- must survive a later user deletion, not a live operational reference.
--
-- local_date is the SAME precompute convention worker_allocations.local_date
-- uses (migration 007, ut-docs#869/#1342): date(collected_at, 'localtime')
-- is resolved ONCE at insert time into a plain column, because SQLite
-- classifies a 'localtime' expression as non-deterministic and refuses to
-- let an index be built on it — while a bare UTC date() match would
-- silently aggregate the wrong calendar day on any non-UTC host, and this
-- table's one market (Turkey, UTC+3) is non-UTC by definition. The index
-- below is what every date-range read (YuzdeUsuluPoolCollectionsTotal,
-- ListYuzdeUsuluPoolCollections) actually uses.
--
-- CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS throughout, same
-- replay-safety convention as every migration since 021.
CREATE TABLE IF NOT EXISTS yuzde_usulu_pool_collections (
    id            TEXT    NOT NULL PRIMARY KEY,
    amount_minor  INTEGER NOT NULL,
    collected_at  TEXT    NOT NULL,
    basis_note    TEXT    NOT NULL DEFAULT '',
    recorded_by   TEXT    NOT NULL,
    local_date    TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_yuzde_usulu_pool_collections_local_date
    ON yuzde_usulu_pool_collections (local_date);

-- Twin archive table (ADR-0042 §1, the exact shape worker_allocations_archive
-- uses in 001_init.sql): no PRIMARY KEY and no UNIQUE — an archive holds the
-- rows of MANY reset batches and must never refuse a re-archived id — and no
-- foreign key except reset_batch_id, which is the one real reference an
-- archived row has (its batch header). reset_archive_repo.go's
-- resetArchiveTables carries the matching column list.
CREATE TABLE IF NOT EXISTS yuzde_usulu_pool_collections_archive (
    id             TEXT    NOT NULL,
    amount_minor   INTEGER NOT NULL,
    collected_at   TEXT    NOT NULL,
    basis_note     TEXT    NOT NULL DEFAULT '',
    recorded_by    TEXT    NOT NULL,
    local_date     TEXT    NOT NULL DEFAULT '',
    reset_batch_id TEXT    NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_yuzde_usulu_pool_collections_archive_batch
    ON yuzde_usulu_pool_collections_archive (reset_batch_id);
