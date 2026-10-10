-- 070_pending_pairings_served_pin.sql — ut-docs#4091 (ADR-0114 §7): the
-- LAN TLS pin the main till served on the connection that carried a pair
-- request ('' when it arrived over plain HTTP: a till without TLS, or an
-- older joining till). The manager's verification code binds it, so a
-- joining till that saw a different certificate (a MITM's) shows a
-- different code. pending_pairings is till-local and never admin-synced.
ALTER TABLE pending_pairings ADD COLUMN served_pin TEXT NOT NULL DEFAULT '';
