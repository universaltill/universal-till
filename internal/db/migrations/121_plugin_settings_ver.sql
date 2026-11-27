-- Per-field version of a plugin setting (shop-config design B, group U2,
-- ADR-0214 amendment): the till half of the cloud's optimistic concurrency.
-- `ver` is monotonic per (plugin, key); `origin` says who wrote the current
-- value (till | cloud | default, default = the manifest default seeded at
-- install, never edited: it has no unreported local edit to protect);
-- `ver_at` is the till's own clock, display-only (ordering is by ver).
-- Existing rows start at ver 1, origin till, exactly what the phase 1 report
-- already said. The value itself is untouched (a secret stays sealed).
ALTER TABLE plugin_settings ADD COLUMN ver INTEGER NOT NULL DEFAULT 1;
ALTER TABLE plugin_settings ADD COLUMN origin TEXT NOT NULL DEFAULT 'till';
ALTER TABLE plugin_settings ADD COLUMN ver_at TEXT;

-- The SumUp key delivery (ADR-0213) kept its counter in the plain setting
-- sumup_key_version: seed the new per-field ver from it once so a replayed
-- older directive stays stale after this upgrade.
UPDATE plugin_settings
   SET ver = (SELECT CAST(v.value_json AS INTEGER) FROM plugin_settings v
               WHERE v.plugin_id = 'sumup' AND v.key = 'sumup_key_version' AND v.scope = 'global'),
       origin = 'cloud'
 WHERE plugin_id = 'sumup' AND scope = 'global' AND key IN ('sumup_api_key', 'sumup_merchant_code')
   AND COALESCE((SELECT CAST(v.value_json AS INTEGER) FROM plugin_settings v
                  WHERE v.plugin_id = 'sumup' AND v.key = 'sumup_key_version' AND v.scope = 'global'), 0) > 0;
