-- 101_payment_method_unknown_placeholder.sql — ut-docs#5804: rows that
-- EnsurePaymentMethod auto-created for an unknown method id were typed 'cash',
-- so shift close / Z / cloud ExpectedCash counted e.g. a plugin card tender
-- synced from another till as drawer cash. Retype exactly those rows:
-- never the seeded 'cash' id, only type 'cash' rows with no plugin owner,
-- sort_order 0 and name == id (or the admin-sync retire-mangled id~id form).
-- Residual, accepted: a LAN-synced plugin row with method_type 'cash', label
-- == key and entry sort_order -100 (not a real configuration).
UPDATE payment_methods SET type = 'unknown'
WHERE type = 'cash'
  AND id <> 'cash'
  AND plugin_id IS NULL
  AND sort_order = 0
  AND (name = id OR name = id || '~' || id);
