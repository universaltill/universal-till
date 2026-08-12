-- Report retention (ADR-0040, ut-docs#571 card 1). Nullable, unset by
-- anything in this card -- card 4 (till<->cloud wiring) is what actually
-- sets it once a report has been acknowledged as stored in the cloud.
ALTER TABLE report_archive ADD COLUMN cloud_acked_at TEXT;
