-- 062: service_charge_tax_basis_bp on sales (ADR-0061 Decision 4, ut-docs#961).
-- The flat tax rate a country plugin's charge.policy.ask answer fixed for the
-- service charge (0 = "apportion across the sale's own per-line rates", the
-- fail-closed default). It has to be PERSISTED, not just threaded through the
-- tender path: computeSaleTotals derives the charge's tax from it, and a
-- replayed/synced sale rebuilds its SaleInput from the persisted row. Without
-- this column a sale tendered at a non-zero basis re-derives a DIFFERENT tax
-- on replay -- and, when the re-derived total lands above the original, the
-- primary rejects the replay outright ("payments do not cover total"), so the
-- sale could never replicate. Storing the basis makes ADR-0061 Decision 4's
-- "replay is exact" true for every answer, not only the no-plugin default.
-- 0 is the correct value for every pre-existing row: no plugin could have
-- answered before this release, so every historical sale was apportioned.
ALTER TABLE sales ADD COLUMN service_charge_tax_basis_bp INTEGER NOT NULL DEFAULT 0;
-- Mirror onto sales_archive, per 040_reset_archive.sql's own header rule (the
-- same rule 056 applied after a reviewer caught 055 missing it): without this,
-- "Clear transaction history" would drop the basis on archive and
-- RestoreResetBatch could never bring it back.
ALTER TABLE sales_archive ADD COLUMN service_charge_tax_basis_bp INTEGER NOT NULL DEFAULT 0;
