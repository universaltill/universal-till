// Package restoredsecrets tells the owner which plugin secrets a restored
// backup (or a LAN join) could not bring back (design-B S7, ADR-0082).
//
// The sealing key file (plugin_settings_key.bin) is excluded from every
// snapshot on purpose, so after a restore each sealed plugin secret reads
// "not configured". Nothing here repairs that, and nothing here ever holds a
// secret: only (plugin id, key) pairs are listed. Each re-entry makes the row
// open again, which removes its item from the list on the next read.
//
// db.ApplyPendingRestore runs before the database opens, so it only drops a
// marker file (MarkPending); the first read of Current after boot scans.
package restoredsecrets

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const (
	// SettingKey holds the JSON list of Item. Non-secret, never reported to
	// the cloud (not a registry entry, so the report allow-list skips it).
	SettingKey = "restore.reenter_list"
	// markerName is the file ApplyPendingRestore leaves next to the DB.
	markerName = "restored-secrets-scan.pending"
	// dismissed is stored in SettingKey instead of a list.
	dismissed = "[]"
)

// Item is one secret that must be entered again. No value, ever.
type Item struct {
	PluginID string `json:"plugin_id"`
	Key      string `json:"key"`
}

// Source reports the stored secrets this till cannot open.
type Source interface {
	UnreadableSecrets(ctx context.Context) ([]Item, error)
}

// Store is the settings store (SettingsRepo satisfies it through an adapter).
type Store interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
}

func markerPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), markerName)
}

// MarkPending records that a restore was applied and a scan is owed. It runs
// before the DB is open, so it is a plain file next to the DB.
func MarkPending(dbPath string) error {
	if dbPath == "" {
		return fmt.Errorf("restored secrets marker: no database path")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("restored secrets marker dir: %w", err)
	}
	return os.WriteFile(markerPath(dbPath), []byte("1\n"), 0o600)
}

// Pending reports whether a scan is owed.
func Pending(dbPath string) bool {
	if dbPath == "" {
		return false
	}
	_, err := os.Stat(markerPath(dbPath))
	return err == nil
}

// Service reads and maintains the list.
type Service struct {
	DBPath string
	Source Source
	Store  Store
}

// Scan stores the current unreadable list and clears the marker.
func (s *Service) Scan(ctx context.Context) ([]Item, error) {
	items, err := s.Source.UnreadableSecrets(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.save(ctx, items); err != nil {
		return nil, err
	}
	// Keep the marker if the write above failed (retry next read).
	if err := os.Remove(markerPath(s.DBPath)); err != nil && !os.IsNotExist(err) {
		return items, fmt.Errorf("clear restored secrets marker: %w", err)
	}
	return items, nil
}

// Current returns what the owner still has to enter again: it runs the owed
// scan first, then drops every stored item that opens now.
func (s *Service) Current(ctx context.Context) ([]Item, error) {
	if Pending(s.DBPath) {
		if _, err := s.Scan(ctx); err != nil {
			return nil, err
		}
	}
	raw, ok, err := s.Store.Get(ctx, SettingKey)
	if err != nil || !ok || raw == "" || raw == dismissed {
		return nil, err
	}
	var stored []Item
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil, nil // unreadable list: show nothing rather than guess
	}
	unreadable, err := s.Source.UnreadableSecrets(ctx)
	if err != nil {
		return nil, err
	}
	still := map[Item]bool{}
	for _, it := range unreadable {
		still[it] = true
	}
	var out []Item
	for _, it := range stored {
		if still[it] {
			out = append(out, it)
		}
	}
	if len(out) != len(stored) {
		if err := s.save(ctx, out); err != nil {
			return out, err
		}
	}
	return out, nil
}

// Dismiss hides the banner until the next restore.
func (s *Service) Dismiss(ctx context.Context) error {
	return s.Store.Set(ctx, SettingKey, dismissed)
}

func (s *Service) save(ctx context.Context, items []Item) error {
	sort.Slice(items, func(i, j int) bool {
		if items[i].PluginID != items[j].PluginID {
			return items[i].PluginID < items[j].PluginID
		}
		return items[i].Key < items[j].Key
	})
	if items == nil {
		items = []Item{}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return s.Store.Set(ctx, SettingKey, string(b))
}
