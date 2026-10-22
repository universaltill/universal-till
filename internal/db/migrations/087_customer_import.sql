-- 087_customer_import.sql — ut-docs#4748 (ADR-0168 §5): the customer write
-- side of the smart import. The sealed import_customers directive reaches
-- the main till only; this schema is what its apply needs.
--
-- customers.postcode: a postcode column of its own (the free-text address
-- stays as it is). It is the second half of the last-resort match (name +
-- postcode, only when exactly one customer fits) and syncs to LAN replicas
-- with the rest of the row; it is personal data and is blanked on an
-- erased-customer shell (sync_admin_repo.go scrubOnRetire).
--
-- Two expression indexes serve the folded matches without scanning the
-- table: lower(email) and the postcode with spaces removed, upper-cased.
-- Both use SQLite's own ASCII-only lower()/upper(); the Go side folds the
-- same way (internal/data/customer_import_repo.go).
--
-- import_source_links (081) gains baseline_hash and erased_at for entity
-- 'customer' links:
--   baseline_hash — a salted HMAC over the fields last imported for that
--                   link, so re-applying the same rows changes nothing;
--   erased_at     — set when the linked customer was erased on this till:
--                   the link stays as a tombstone (local_id blanked) so a
--                   re-import skips that person and counts erased_on_till.
--
-- customer_import_salt: this till's own random salt (one row), created on
-- first use. It never leaves the till; it keys the baseline and the
-- erasure hashes so neither can be recomputed from a guessed email.
--
-- customer_erasure_hashes: a suppression list. When a customer is erased,
-- the salted hashes of their email and phone are kept (kind 'email' /
-- 'phone') so a later import of the same address or number is skipped, not
-- recreated. Pseudonymous data; the legal pass (ADR-0168 §5) covers its
-- basis. Main-till bookkeeping, never synced, never sent to the cloud.
--
-- Migrations are frozen the moment they merge to main (ADR-0100) — never
-- edit this file; append a new NNN_*.sql instead.
ALTER TABLE customers ADD COLUMN postcode TEXT;

CREATE INDEX IF NOT EXISTS idx_customers_email_lower ON customers (lower(email));
CREATE INDEX IF NOT EXISTS idx_customers_postcode_fold ON customers (replace(upper(postcode), ' ', ''));

ALTER TABLE import_source_links ADD COLUMN baseline_hash TEXT;
ALTER TABLE import_source_links ADD COLUMN erased_at TEXT;

-- An erasure finds a customer's links by (entity, local_id); 081's index
-- starts with source.
CREATE INDEX IF NOT EXISTS idx_import_source_links_entity_local
    ON import_source_links (entity, local_id);

CREATE TABLE IF NOT EXISTS customer_import_salt (
    id   INTEGER PRIMARY KEY CHECK (id = 1),
    salt BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS customer_erasure_hashes (
    hash      TEXT PRIMARY KEY,
    kind      TEXT NOT NULL CHECK (kind IN ('email', 'phone')),
    erased_at TEXT NOT NULL
);
