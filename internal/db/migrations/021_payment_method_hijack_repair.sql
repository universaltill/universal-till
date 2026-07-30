-- Repair built-in tenders captured by a plugin payment-entry key collision.
-- Before 2026-07-30, SyncPluginPaymentMethods' ON CONFLICT(id) DO UPDATE
-- stamped plugin_id onto whatever payment_methods row already owned the
-- plugin entry's key — a plugin declaring {type:"payment", key:"cash"}
-- captured the seeded cash tender, and the sync's deactivate step then
-- flipped the built-in inactive once the plugin was disabled/removed
-- (cash silently vanished from checkout). The sync is now ownership-
-- guarded; this restores any till already damaged. Only the three seeded
-- built-ins are targeted: for them plugin_id can ONLY mean a capture
-- (they predate every plugin). Names deliberately left as-is — an
-- operator can rename a tender, so a name reset could destroy real
-- configuration; ownership + availability are what checkout needs.
UPDATE payment_methods
SET plugin_id = NULL,
    is_active = 1
WHERE id IN ('cash', 'card', 'gift')
  AND plugin_id IS NOT NULL;

-- Disarm the squatting entries themselves: both payment dispatch gates
-- match on entry key alone, so a legacy plugin entry with key 'cash'
-- would still receive payment.cash.* events for every cash tender and —
-- if it subscribes the authorize leg — could decline them. New installs
-- with these keys are rejected outright; this covers what's already on
-- disk from before the fix.
UPDATE plugin_entries
SET is_active = 0
WHERE type = 'payment'
  AND key IN ('cash', 'card', 'gift');
