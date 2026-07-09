-- Audit entries written by the till itself (stock receipts, overrides, …) use
-- actor 'system' until real session management lands; audit_log.actor_id has
-- an FK to users, so the actor must exist.
INSERT OR IGNORE INTO users (id, username, display_name, role, is_active)
VALUES ('system', 'system', 'System', 'admin', 1);
