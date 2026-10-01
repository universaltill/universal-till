-- 054_roles_label_origin.sql — ADR-0128 §2 (ut-docs#3165): custom staff
-- roles are created in my.universaltill.com and delivered to the main till
-- as save_role / delete_role directives; the admin bundle carries them to
-- every other till.
--
--   * label: the shop's own name for a custom role, stored verbatim in the
--     shop's language. Built-in roles keep '' and keep their translated
--     labels from the users.role.<key> locale keys.
--   * origin: 'builtin' | 'cloud'. A custom role's key never changes
--     (c_ + a ULID minted by the cloud), so a rename only rewrites label —
--     never users.role or role_permissions on any till.
--
-- Same shape as 040/043: ADD COLUMN ... NOT NULL DEFAULT so nothing on an
-- upgrading till changes. The backfill covers a satellite till that
-- received custom-role rows while it was still on an older version, before
-- it had the column: they would otherwise stay 'builtin', because the
-- fingerprint-gated admin pull does not re-apply an unchanged bundle. The
-- LIKE escapes the underscore so only the c_ key shape matches.
ALTER TABLE roles ADD COLUMN label TEXT NOT NULL DEFAULT '';
ALTER TABLE roles ADD COLUMN origin TEXT NOT NULL DEFAULT 'builtin';
UPDATE roles SET origin = 'cloud' WHERE role LIKE 'c\_%' ESCAPE '\';
