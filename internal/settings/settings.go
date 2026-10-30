package settings

import (
	"context"
	"database/sql"

	"github.com/universaltill/universal-till/internal/data"
)

type Store struct {
	repo *data.SettingsRepo
}

func NewStore(db *sql.DB) *Store {
	return &Store{repo: data.NewSettingsRepo(db)}
}

// raw helpers

func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	return s.repo.Get(ctx, key)
}

func (s *Store) Set(ctx context.Context, key, value string) error {
	return s.repo.Set(ctx, key, value)
}

// SyncBearerPrev, RotateSyncBearer and AdoptPrevSyncBearer are the
// data.SettingsRepo methods of the same names (ut-docs#5203): the replica's
// previous LAN bearer, kept so a main till restored from an older backup can
// still be reached.
func (s *Store) SyncBearerPrev(ctx context.Context) (string, error) {
	return s.repo.SyncBearerPrev(ctx)
}

func (s *Store) RotateSyncBearer(ctx context.Context, old, fresh string) (bool, error) {
	return s.repo.RotateSyncBearer(ctx, old, fresh)
}

func (s *Store) AdoptPrevSyncBearer(ctx context.Context, failed string) (string, bool, error) {
	return s.repo.AdoptPrevSyncBearer(ctx, failed)
}

// GetOrCreate is data.SettingsRepo.GetOrCreate: the stored value, or
// defaultValue persisted atomically when the key is absent. It makes Store an
// enroll.Settings / discovery.SettingsReader (ut-docs#3307).
func (s *Store) GetOrCreate(ctx context.Context, key, defaultValue string) (string, error) {
	return s.repo.GetOrCreate(ctx, key, defaultValue)
}

// GetByPrefix returns every key/value pair whose key starts with prefix, in
// one query — see SettingsRepo.GetByPrefix.
func (s *Store) GetByPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	return s.repo.GetByPrefix(ctx, prefix)
}

// SetMany upserts every pair in one transaction — all or nothing.
func (s *Store) SetMany(ctx context.Context, kv map[string]string) error {
	return s.repo.SetMany(ctx, kv)
}

// All returns all key/value pairs from settings.
func (s *Store) All(ctx context.Context) (map[string]string, error) {
	return s.repo.All(ctx)
}
