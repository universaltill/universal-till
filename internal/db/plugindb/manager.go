package plugindb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// SoftDeleteDays is how long an ordinary plugin's data is kept after
	// uninstall (ADR-0121 §8); a reinstall restores it.
	SoftDeleteDays = 30
	removedDir     = ".removed"
	markerName     = "removed.json"
)

var (
	// ErrRetained: the data is inside its declared retention period and may
	// not be deleted yet (fiscal records, retention.keep_years).
	ErrRetained = errors.New("plugindb: data is within its retention period")
	// ErrInvalidID: not a safe single path segment.
	ErrInvalidID = errors.New("plugindb: invalid plugin id")
	// ErrNotRemoved: nothing soft-deleted for that plugin.
	ErrNotRemoved = errors.New("plugindb: no removed data for this plugin")
	// ErrExists: live data already exists, refusing to overwrite it.
	ErrExists = errors.New("plugindb: plugin data already exists")

	idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	// removedPattern is the strict name of a soft-deleted tree: <id>-YYYYMMDD.
	removedPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*-[0-9]{8}$`)
)

func validID(id string) error {
	// A trailing dot is refused on top of the ADR-0121 §2 pattern: Windows
	// drops it from a directory name, so "shop." would open "shop"'s data.
	if !idPattern.MatchString(id) || strings.Contains(id, "..") || strings.HasSuffix(id, ".") {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return nil
}

// Manager owns the <data>/plugin-data root. root is paths.Data("plugin-data").
type Manager struct {
	root string
	now  func() time.Time
	mu   sync.Mutex
	open map[string]*Store
}

// NewManager returns a Manager for root. now may be nil (time.Now).
func NewManager(root string, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{root: root, now: now, open: map[string]*Store{}}
}

func (m *Manager) dir(id string) string { return filepath.Join(m.root, id) }

// Open returns the plugin's own Store, creating the file and applying pending
// migrations. A second Open for the same id returns the live Store. The
// plugin id is the only thing choosing the path, and it must be one safe
// segment; there is no way to name another plugin's file.
func (m *Manager) Open(ctx context.Context, id string, o Options) (*Store, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.open[id]; ok && s.db != nil {
		// Re-validate migrations (an upgrade passes a longer set).
		s.mu.Lock()
		err := migrate(ctx, s.conn, o.Migrations, m.now())
		s.mu.Unlock()
		return s, err
	}
	s, err := openStore(ctx, id, m.dir(id), o, m.now())
	if err != nil {
		return nil, err
	}
	m.open[id] = s
	return s, nil
}

// Close closes one plugin's Store (unload/disable); the data stays.
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeLocked(id)
}

func (m *Manager) closeLocked(id string) error {
	if s, ok := m.open[id]; ok {
		delete(m.open, id)
		return s.Close()
	}
	return nil
}

// CloseAll closes every open Store (shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.open {
		_ = m.closeLocked(id)
	}
}

// removedMarker is written next to a soft-deleted tree.
type removedMarker struct {
	PluginID    string    `json:"plugin_id"`
	RemovedAt   time.Time `json:"removed_at"`
	PurgeAfter  time.Time `json:"purge_after"`
	RetainUntil time.Time `json:"retain_until,omitempty"`
	KeepYears   int       `json:"keep_years,omitempty"`
}

func (mk removedMarker) deletableAt() time.Time {
	if mk.RetainUntil.After(mk.PurgeAfter) {
		return mk.RetainUntil
	}
	return mk.PurgeAfter
}

// Uninstall closes the plugin's database and moves its whole data tree to
// .removed/<id>-<YYYYMMDD>. Nothing is deleted: the data is kept for
// SoftDeleteDays (a reinstall restores it) and, when the manifest declares
// retention.keep_years, for that many years with deletion refused meanwhile.
// It touches only plugin-data; the code tree is uninstalled separately.
func (m *Manager) Uninstall(id string, keepYears int) (string, error) {
	if err := validID(id); err != nil {
		return "", err
	}
	if keepYears < 0 {
		return "", fmt.Errorf("plugindb: negative keep_years")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.closeLocked(id); err != nil {
		return "", err
	}
	src := m.dir(id)
	if _, err := os.Lstat(src); err != nil {
		if os.IsNotExist(err) {
			return "", nil // plugin never had data
		}
		return "", err
	}
	now := m.now().UTC()
	rdir := filepath.Join(m.root, removedDir)
	if err := os.MkdirAll(rdir, 0o700); err != nil {
		return "", err
	}
	base := filepath.Join(rdir, id+"-"+now.Format("20060102"))
	dst := base
	for i := 2; ; i++ { // same-day reinstall+uninstall: never overwrite
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			break
		}
		if i > 1000 {
			return "", ErrExists
		}
		dst = fmt.Sprintf("%s.%d", base, i)
	}
	mk := removedMarker{PluginID: id, RemovedAt: now, PurgeAfter: now.AddDate(0, 0, SoftDeleteDays), KeepYears: keepYears}
	if keepYears > 0 {
		mk.RetainUntil = now.AddDate(keepYears, 0, 0)
	}
	b, _ := json.Marshal(mk)
	if err := os.WriteFile(filepath.Join(src, markerName), b, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// latestRemoved finds the newest soft-deleted tree for id with a marker.
func (m *Manager) latestRemoved(id string) (string, removedMarker, error) {
	ents, err := os.ReadDir(filepath.Join(m.root, removedDir))
	if err != nil {
		if os.IsNotExist(err) {
			return "", removedMarker{}, ErrNotRemoved
		}
		return "", removedMarker{}, err
	}
	var best string
	var bestMk removedMarker
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, id+"-") {
			continue
		}
		mk, ok := readMarker(filepath.Join(m.root, removedDir, name))
		if !ok || mk.PluginID != id {
			continue
		}
		if best == "" || mk.RemovedAt.After(bestMk.RemovedAt) {
			best, bestMk = name, mk
		}
	}
	if best == "" {
		return "", removedMarker{}, ErrNotRemoved
	}
	return filepath.Join(m.root, removedDir, best), bestMk, nil
}

func readMarker(dir string) (removedMarker, bool) {
	b, err := os.ReadFile(filepath.Join(dir, markerName))
	if err != nil {
		return removedMarker{}, false
	}
	var mk removedMarker
	if json.Unmarshal(b, &mk) != nil || mk.PluginID == "" {
		return removedMarker{}, false
	}
	return mk, true
}

// Restore moves the newest soft-deleted data back (reinstall). It refuses to
// overwrite live data.
func (m *Manager) Restore(id string) error {
	if err := validID(id); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := os.Lstat(m.dir(id)); err == nil {
		return ErrExists
	}
	src, _, err := m.latestRemoved(id)
	if err != nil {
		return err
	}
	// Move first, drop the marker after: a failed move must leave the tree
	// marked, or it could never be restored or purged again.
	if err := os.Rename(src, m.dir(id)); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(m.dir(id), markerName))
	return nil
}

// DeleteNow is the operator's explicit "delete data now" for removed data. It
// is refused with ErrRetained while a keep_years period runs. The GB logbook
// hard delete is the plugin's own typed-confirm flow and does not go through
// here.
func (m *Manager) DeleteNow(id string) error {
	if err := validID(id); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dir, mk, err := m.latestRemoved(id)
	if err != nil {
		return err
	}
	if m.now().Before(mk.RetainUntil) {
		return fmt.Errorf("%w: until %s", ErrRetained, mk.RetainUntil.Format("2006-01-02"))
	}
	return os.RemoveAll(dir)
}

// Prune purges soft-deleted trees whose SoftDeleteDays and retention period
// have both passed and returns the purged directory names. Only directories
// directly under .removed with a strict <id>-YYYYMMDD name and a valid marker
// are candidates; anything else is left alone. The caller schedules it; it is
// not wired to a timer in slice A.
func (m *Manager) Prune() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ents, err := os.ReadDir(filepath.Join(m.root, removedDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var purged []string
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 || !(removedPattern.MatchString(name) || removedSuffixed.MatchString(name)) {
			continue
		}
		dir := filepath.Join(m.root, removedDir, name)
		mk, ok := readMarker(dir)
		if !ok || m.now().Before(mk.deletableAt()) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return purged, err
		}
		purged = append(purged, name)
	}
	return purged, nil
}

var removedSuffixed = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*-[0-9]{8}\.[0-9]+$`)
