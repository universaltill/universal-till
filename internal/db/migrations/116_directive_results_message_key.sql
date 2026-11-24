-- 116_directive_results_message_key.sql — ut-docs#6975: the en.json message key
-- (plugins.install.error.*) a failed install_plugin directive reported, stored
-- with its result so a re-posted result (the first post failed, the cloud
-- re-delivered the directive) carries the same key. '' = none. A key, never
-- free text. Re-runnable: the runner makes ADD COLUMN idempotent.
ALTER TABLE directive_results ADD COLUMN message_key TEXT NOT NULL DEFAULT '';
