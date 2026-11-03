-- 093_tills_bearer_rotation.sql — ut-docs#5198: rotation of a replica's LAN
-- sync bearer. A bearer never expired and never changed, so a copy that
-- leaked once (an old backup, a stolen disk) stayed valid for as long as the
-- till stayed paired. The main till now asks a replica to swap its bearer
-- when it is old or has never been rotated, and keeps the previous one
-- working for a short grace window so a replica that missed the answer is
-- not locked out.
--
--   bearer_issued_at       when the current bearer was issued (RFC 3339 UTC);
--                          NULL = a bearer from before rotation existed, so
--                          every till rotates once after the upgrade.
--   bearer_prev_hash       the bearer hash this one replaced, still accepted.
--   bearer_prev_expires_at when the previous bearer stops working. NULL while
--                          the new bearer has not been used yet (the grace
--                          window starts at its first use, not at rotation).
--
-- All three are till-local secrets/state: redacted from the admin bundle and
-- the join snapshot, and no trigger bumps sync_admin_version for them.
ALTER TABLE tills ADD COLUMN bearer_issued_at TEXT;
ALTER TABLE tills ADD COLUMN bearer_prev_hash TEXT;
ALTER TABLE tills ADD COLUMN bearer_prev_expires_at TEXT;
