-- German till-registration bookkeeping: §146a Abs. 4 AO (ut-docs#665).
--
-- Since 2020, every electronic recording system used in Germany (incl. a
-- POS till and its TSE) must be reported to the responsible Finanzamt via
-- Mein ELSTER -- one notification per till, naming both the till (the
-- "elektronisches Aufzeichnungssystem", eAS) and its TSE (the certified
-- fiscal signing device, §146a Abs. 1 AO / KassenSichV). This migration
-- adds nowhere to FILE that notification -- Universal Till does not submit
-- anything to ELSTER on the shop's behalf (out of scope, ut-docs#937 is the
-- separate export/XML follow-up) -- only somewhere to RECORD the data the
-- shop needs on hand to file it themselves: the fields ELSTER's own form
-- asks for, one row per till.
--
-- fiscal_register_de is deliberately its own table, not columns bolted onto
-- `registers`: a till can go through this DE-specific bookkeeping more than
-- once in its life (a TSE swap after a hardware failure, say), and every
-- prior entry must stay on record, not be overwritten -- see
-- decommissioned_on below. One row = one eAs/TSE pairing that was, or still
-- is, registered against a till.
--   * eas_type/eas_software/eas_serial: identify the till itself (the
--     "elektronisches Aufzeichnungssystem") -- ELSTER's form separates the
--     system category (eas_type, e.g. "Tablet-/App-Kassen-Systeme") from
--     the software/serial identifying the concrete installation.
--   * tse_serial/tse_certification_id/tse_type: identify the certified TSE
--     module attached to it (BSI certification id + the serial number
--     printed on/reported by the device -- ADR-0045 already tracks TSE
--     ownership models elsewhere; this is the registration paperwork, not
--     a new ownership record).
--   * acquired_on: when the till entered service (ELSTER's "Datum der
--     Anschaffung"), NOT NULL -- always known and always required by the
--     form.
--   * commissioned_on/decommissioned_on: when the till went into, and later
--     came out of, live productive use -- both optional (a till can be
--     acquired before it's commissioned, and most rows never reach
--     decommissioned at all). Nulling either back out is never done by this
--     schema or the page above it -- ADR-0042's "destroys nothing" principle
--     applies here too, in spirit: marking a till decommissioned records a
--     date, it never deletes or hides the row (the register stays visible,
--     status flips) -- the AO obligation these rows exist for outlives the
--     till itself.
--
-- All four dates are stored as ISO-8601 YYYY-MM-DD strings (this repo's
-- plain-date convention, matching e.g. tax_codes' starts_at or the reset
-- archive's retained-until dates) -- these are calendar dates a shop owner
-- typed on a form, not instants, so no time-of-day or timezone component
-- applies. created_at/updated_at stay this repo's usual RFC3339 UTC
-- timestamps (e.g. audit_log.created_at), since those ARE instants -- the
-- moment the bookkeeping record itself was written or last touched.
--
-- register_id REFERENCES registers(id) with no ON DELETE clause: registers
-- are soft-deleted (is_active), never hard-deleted, so this FK is never
-- exercised as a cascade path -- it exists purely to catch a typo'd/stale
-- register id at write time, same as fiscal_tse_signatures' sale_id FK
-- (048_fiscal_tse_signatures.sql).
--
-- The three stock_locations address columns are added here rather than in
-- their own migration because they exist for exactly this feature: ELSTER's
-- form also asks for the till's place of business, and a location's address
-- was simply never captured before now -- nothing else in the schema needed
-- it. All three are nullable; an existing location's address is unknown
-- until a manager fills it in on the new page, and that must not block
-- anything about the location itself (stock movements, register
-- assignment, ...) that already works today.
ALTER TABLE stock_locations ADD COLUMN address_street TEXT;
ALTER TABLE stock_locations ADD COLUMN address_postcode TEXT;
ALTER TABLE stock_locations ADD COLUMN address_city TEXT;

CREATE TABLE fiscal_register_de (
    id                    TEXT PRIMARY KEY,
    register_id           TEXT NOT NULL REFERENCES registers(id),
    eas_type              TEXT NOT NULL,
    eas_software          TEXT NOT NULL,
    eas_serial            TEXT NOT NULL,
    tse_serial            TEXT NOT NULL,
    tse_certification_id  TEXT NOT NULL,
    tse_type              TEXT NOT NULL,
    acquired_on           TEXT NOT NULL,
    commissioned_on       TEXT,
    decommissioned_on     TEXT,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);
CREATE INDEX idx_fiscal_register_de_register ON fiscal_register_de(register_id);
