-- 061: tip_recipient on payments (ADR-0061 Decision 3, ut-docs#961). Whose
-- money a tip is for tax purposes is country law, not merchant preference:
-- Germany's DSFinV-K export needs TrinkgeldAN vs TrinkgeldAG, the UK's
-- Allocation of Tips Act 2023 needs it for allocation records (ut-docs#964,
-- separate card). Persisted per payment, alongside 019's tip_amount, so a
-- report built later reads the recipient AS IT WAS at capture time, never
-- recomputed from a policy that may have since changed. 'employee' is the
-- one default every researched market agrees on and is what applies when
-- nothing (no plugin answer, an old journal replay) says otherwise.
ALTER TABLE payments ADD COLUMN tip_recipient TEXT NOT NULL DEFAULT 'employee';
-- Mirror onto payments_archive, per 040_reset_archive.sql's own header rule
-- (same reason 051 mirrored 050): without this, "Clear transaction history"
-- would silently drop the recipient on archive and RestoreResetBatch could
-- never bring it back.
ALTER TABLE payments_archive ADD COLUMN tip_recipient TEXT NOT NULL DEFAULT 'employee';
