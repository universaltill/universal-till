package settings

import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
)

// Per-field version of core settings: delegates to data.SettingsRepo
// (settings_repo_meta.go).

func (s *Store) SettingMeta(ctx context.Context, key string) (data.SettingMeta, error) {
	return s.repo.SettingMeta(ctx, key)
}

func (s *Store) SettingMetaAll(ctx context.Context) (map[string]data.SettingMeta, error) {
	return s.repo.SettingMetaAll(ctx)
}

func (s *Store) SetFromCloud(ctx context.Context, key, value string, ver, baseVer *int64) error {
	return s.repo.SetFromCloud(ctx, key, value, ver, baseVer)
}

func (s *Store) CheckCloudVersion(ctx context.Context, key string, ver, baseVer *int64) error {
	return s.repo.CheckCloudVersion(ctx, key, ver, baseVer)
}

func (s *Store) MarkCloudApplied(ctx context.Context, key string, ver *int64) error {
	return s.repo.MarkCloudApplied(ctx, key, ver)
}
