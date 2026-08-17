-- 052: one-off repair for pre-existing duplicate scope='global'
-- plugin_settings rows (ut-docs#788).
--
-- plugin_settings' UNIQUE(plugin_id, key, scope, scope_id) doesn't catch
-- duplicate global rows because scope_id is NULL for them and SQLite
-- treats NULLs as distinct in a unique index. ut-docs#785 closed the race
-- in UpsertPluginSettingScoped that created them (wrapped in its own
-- transaction), so no NEW duplicates form; this migration repairs a till
-- that already has some from before that fix, rather than leaving it to
-- self-heal lazily on the plugin's next install/upgrade
-- (PluginRepo.ReconcilePluginSettings, called from PersistManifest).
--
-- Keep the most-recently-updated row per (plugin_id, key) among
-- scope='global' rows; delete the rest. Ties break on id (deterministic,
-- reproducible across tills) since updated_at can collide at
-- second-resolution. This is a simpler tiebreak than
-- ReconcilePluginSettings' "non-default value beats default" rule --
-- deliberately so: this migration has no access to each plugin's manifest
-- defaults to replicate that rule, so "keep the latest write" is the best
-- available deterministic fallback (GetPluginSetting, plugin_repo.go, is
-- the codebase's other place a single winner must be picked among several
-- candidate rows, though it orders by scope specificity for reads, not
-- recency -- there's no existing recency-tiebreak precedent to reuse
-- here, this is a fresh call for this migration). Register/user-scoped
-- rows are also stored with a NULL scope_id today, so the same schema gap
-- could in principle affect them too, but #785's own review scoped the
-- finding to 'global' specifically (the only scope with realistic
-- concurrent writers) -- this migration mirrors that scope rather than
-- widening it.
DELETE FROM plugin_settings
WHERE scope = 'global' AND scope_id IS NULL
  AND id NOT IN (
    SELECT id FROM (
      SELECT id,
             ROW_NUMBER() OVER (
               PARTITION BY plugin_id, key
               ORDER BY updated_at DESC, id DESC
             ) AS rn
      FROM plugin_settings
      WHERE scope = 'global' AND scope_id IS NULL
    )
    WHERE rn = 1
  );
