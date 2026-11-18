-- ut-docs#5658: durable record of marketplace-revoked plugin releases.
-- No FK to plugins on purpose: the record must survive uninstall so a
-- cached bundle of a revoked release cannot be re-imported. Rewritten from
-- the revocation feed on every successful poll (a release the feed no
-- longer lists is lifted). version '' = every version.
CREATE TABLE IF NOT EXISTS plugin_revoked_releases (
  manifest_id TEXT NOT NULL,
  version     TEXT NOT NULL DEFAULT '',
  listing_id  TEXT NOT NULL DEFAULT '',
  reason      TEXT NOT NULL DEFAULT '',
  action      TEXT NOT NULL DEFAULT '',
  recorded_at TEXT NOT NULL,
  PRIMARY KEY (manifest_id, version)
);
