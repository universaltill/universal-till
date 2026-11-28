package plugindb

import "path/filepath"

// Path is the database file; only tests need it (the host never exposes it).
func (s *Store) Path() string { return s.path }

func (m *Manager) dir(id string) string { return filepath.Join(m.root, id) }
