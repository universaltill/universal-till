-- 114_card_readers_transport.sql — ut-docs#6756 (ADR-0205 §1, §5): `kind` says who
-- drives a reader, `transport` how it reaches the device that drives it; core
-- validates the pair in Go (internal/data, no CHECK, as for `kind`).
-- device_serial / bound_at: the provider-reported reader serial and the UTC
-- time of an authenticated bind (§5); NULL = never bound. A reader id, not
-- card data (ADR-0127). Re-runnable: the runner makes ADD COLUMN idempotent,
-- both UPDATEs are no-ops on a second run.
ALTER TABLE card_readers ADD COLUMN transport TEXT NOT NULL DEFAULT 'internet';
ALTER TABLE card_readers ADD COLUMN device_serial TEXT;
ALTER TABLE card_readers ADD COLUMN bound_at TEXT;
-- The `bluetooth` kind is retired (ADR-0205 §1): who drives it is the SDK.
UPDATE card_readers SET kind = 'sdk', transport = 'bluetooth' WHERE kind = 'bluetooth';
UPDATE card_readers SET transport = 'device_own' WHERE kind = 'tap_to_pay';
-- cloud/phone keep the default 'internet'. Any other hand-edited kind keeps
-- 'internet' and fails the Go pair check: never offered (fail closed, §8).
