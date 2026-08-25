-- 064: worker_allocations ledger (ADR-0063, ut-docs#987). Shared
-- record-keeping primitive for two independent statutory obligations —
-- UK Employment (Allocation of Tips) Act 2023 (ut-docs#964) and Turkey
-- İş Kanunu 4857 art. 51 "yüzde usulü" (ut-docs#965) — both of which
-- require money collected to be recorded as distributed in full to named
-- workers, retained, and producible on request. Neither obligation is
-- about how much is collected or taxed (payments.tip_amount/tip_recipient
-- from ADR-0061, sale_charges from ADR-0062 already cover that) — this
-- table is only about what happens to the money afterward.
--
-- cashier_id reuses ShiftInput.CashierID's existing worker identity (no
-- new staff concept, per ADR-0063 Decision 1). source_id is a soft
-- reference (payment_id, sale_id, or a pool-batch identifier for a
-- Turkey yüzde usulü distribution with no underlying bill line) —
-- deliberately NOT a foreign key: a soft reference cannot break or block
-- on a catalog/customer/sale row that a later reset archives or a later
-- cleanup removes (ADR-0063 Decision 1 / Consequences).
-- source_type CHECK (independent review, ut-docs#987): the write side
-- (InsertWorkerAllocation) stores whatever string it's handed, and the
-- read side (WorkerAllocationsSummary) rejects any source_type outside the
-- three known values — without this constraint a caller typo would write
-- a statutory record that silently can never be read back through the
-- sanctioned query path. Single-table CHECKs are one of the archive-table
-- relaxations ADR-0042 §1 keeps (only FK and PK/UNIQUE are relaxed), so
-- worker_allocations_archive keeps this CHECK too, below.
CREATE TABLE IF NOT EXISTS worker_allocations (
    id            TEXT    NOT NULL PRIMARY KEY,
    source_type   TEXT    NOT NULL CHECK (source_type IN ('tip', 'service_charge', 'yuzde_usulu_pool')),
    source_id     TEXT    NOT NULL DEFAULT '',   -- payment_id/sale_id, or a pool-batch id; '' for none
    cashier_id    TEXT    NOT NULL,              -- worker who received this allocation
    amount_minor  INTEGER NOT NULL,              -- allocated amount, integer minor units (money.Money at the boundary)
    allocated_at  TEXT    NOT NULL,              -- when recorded — may postdate the sale/payment (a shift-end payout)
    note          TEXT    NOT NULL DEFAULT ''    -- free text: a TR pool's basis or a UK policy reference
);

CREATE INDEX IF NOT EXISTS idx_worker_allocations_cashier ON worker_allocations (cashier_id);
CREATE INDEX IF NOT EXISTS idx_worker_allocations_source ON worker_allocations (source_type, source_id);
CREATE INDEX IF NOT EXISTS idx_worker_allocations_allocated_at ON worker_allocations (allocated_at);

-- Archive twin (ADR-0042 §1 / 040_reset_archive.sql's own header rule):
-- column-identical to the live table plus reset_batch_id, no FK to live
-- tables, no PK/UNIQUE (a shop that resets, trades, and resets again must
-- be able to archive the same worker_allocations content twice across
-- different batches).
CREATE TABLE IF NOT EXISTS worker_allocations_archive (
    id            TEXT    NOT NULL,
    source_type   TEXT    NOT NULL CHECK (source_type IN ('tip', 'service_charge', 'yuzde_usulu_pool')),
    source_id     TEXT    NOT NULL DEFAULT '',
    cashier_id    TEXT    NOT NULL,
    amount_minor  INTEGER NOT NULL,
    allocated_at  TEXT    NOT NULL,
    note          TEXT    NOT NULL DEFAULT '',
    reset_batch_id TEXT   NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_worker_allocations_archive_batch ON worker_allocations_archive (reset_batch_id);
