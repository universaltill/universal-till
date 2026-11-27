-- 118_pending_pairings_requester_device.sql — ut-docs#7097 (AC16, ut-docs#7057).
-- A joining till now sends its own device id with the pair request, so one
-- device has at most one pending request (a retry replaces the older row
-- instead of piling up three for the manager). '' = an older till that sent no
-- id: behaviour unchanged. pending_pairings is till-local, never admin-synced.
ALTER TABLE pending_pairings ADD COLUMN requester_device_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_pending_pairings_requester ON pending_pairings(requester_device_id) WHERE requester_device_id <> '';
-- The enrolled till remembers the device id it joined with, so a re-join under
-- a new name updates its row instead of adding a second one (rows enrolled
-- before this keep matching by name).
ALTER TABLE tills ADD COLUMN device_id TEXT; -- nullable: an admin-bundle redactCol is force-NULLed on a replica
