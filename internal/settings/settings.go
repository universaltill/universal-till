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

// All returns all key/value pairs from settings.
func (s *Store) All(ctx context.Context) (map[string]string, error) {
	return s.repo.All(ctx)
}
