-- 064: sale_charges — the itemized additive statutory charge list (ADR-0062,
-- ut-docs#963/#984). Step 1 of 3 landing ADR-0062: this migration lands the
-- schema only; nothing writes to this table until step 2 (ut-docs#985) wires
-- computeSaleTotals/recomputeTotals/the tender handler to build a
-- []ChargeInput and persist it here alongside sales.
--
-- Replaces the single-slot sales.service_charge_amount /
-- service_charge_tax_basis_bp as the source of truth for tax computation —
-- those two columns STAY (migrations are append-only) but become
-- display/reconciliation-only derived fields once step 2 lands (ADR-0062
-- Decision 2): service_charge_amount = sum of every charge's amount,
-- service_charge_tax_basis_bp = the first charge's tax_basis_bp, meaningful
-- only when a sale has exactly one charge. No reader may derive tax from
-- that pair once 2+ charges exist — read sale_charges instead.
--
-- No FK to sale_charges_archive, no PK/UNIQUE on it, per ADR-0042 §1's
-- archive-twin relaxations (via 040_reset_archive.sql's header comment) —
-- mirrors sale_discounts/sale_discounts_archive exactly.
CREATE TABLE IF NOT EXISTS sale_charges (
    sale_id      TEXT    NOT NULL REFERENCES sales (id),
    seq          INTEGER NOT NULL,   -- display/apportionment order
    key          TEXT    NOT NULL,   -- "service_charge" is core's reserved key
    label        TEXT    NOT NULL DEFAULT '', -- '' = core's own default label
    amount_minor INTEGER NOT NULL,   -- already-computed, persisted verbatim
                                      -- (never recomputed on replay — same
                                      -- reasoning as ADR-0061 Decision 4)
    tax_basis_bp INTEGER NOT NULL DEFAULT 0, -- 0 = apportion at the sale's
                                      -- own per-line rates
    base         TEXT    NOT NULL DEFAULT 'net_lines',
    PRIMARY KEY (sale_id, seq)
);

CREATE TABLE IF NOT EXISTS sale_charges_archive (
    sale_id        TEXT    NOT NULL,
    seq            INTEGER NOT NULL,
    key            TEXT    NOT NULL,
    label          TEXT    NOT NULL DEFAULT '',
    amount_minor   INTEGER NOT NULL,
    tax_basis_bp   INTEGER NOT NULL DEFAULT 0,
    base           TEXT    NOT NULL DEFAULT 'net_lines',
    reset_batch_id TEXT    NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_sale_charges_archive_batch ON sale_charges_archive (reset_batch_id);
