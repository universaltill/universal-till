package ui

import (
	"os"
	"path/filepath"
	"sync"
)

type FileButtonStore struct {
	path string
	mu   sync.RWMutex
}

func NewFileButtonStore(dataDir string) *FileButtonStore {
	return &FileButtonStore{path: filepath.Join(dataDir, "buttons.json")}
}

// NewButtonStore selects storage by env UT_STORE ("sqlite" to use SQLite). Defaults to file JSON.
func NewButtonStore(dataDir string) ButtonStore {
	if os.Getenv("UT_STORE") == "sqlite" {
		if s, err := NewSQLiteButtonStore(filepath.Join(dataDir, "unitill.db")); err == nil {
			return s
		}
	}
	return &FileButtonStore{path: filepath.Join(dataDir, "buttons.json")}
}
