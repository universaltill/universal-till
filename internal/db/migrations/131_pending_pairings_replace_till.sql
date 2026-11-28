-- 131_pending_pairings_replace_till.sql: ut-docs#7098 (epic #7097). The manager
-- may approve a join as "replace this old entry" (the same device coming back
-- under a new name): the chosen tills.id is stored with the approval and read
-- back by /api/sync/enroll through the approval token, so the choice is bound
-- to that one request. '' = a new device (today's behaviour). pending_pairings
-- is till-local and never synced.
ALTER TABLE pending_pairings ADD COLUMN replace_till_id TEXT NOT NULL DEFAULT '';
