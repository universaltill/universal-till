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

// windowsReserved reports a Windows device name (with or without extension).
func windowsReserved(id string) bool {
	base := id
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "con", "prn", "aux", "nul":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '0' && base[3] <= '9'
}

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
	if !idPattern.MatchString(id) || strings.Contains(id, "..") || strings.HasSuffix(id, ".") || windowsReserved(id) {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return nil
}

// Manager owns the <data>/plugin-data root. root is paths.Data("plugin-data").
// mu only guards the maps; slow work holds a per-id lock (lock.go).
type Manager struct {
	root      string
	now       func() time.Time
	mu        sync.Mutex
	open      map[string]*Store
	locks     map[string]*idLock
	removedMu sync.Mutex // serialises everything under .removed
}

// NewManager returns a Manager for root. now may be nil (time.Now).
func NewManager(root string, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{root: root, now: now, open: map[string]*Store{}, locks: map[string]*idLock{}}
}

// Open returns the plugin's own Store, creating the file and (unless
// o.NoMigrate) applying pending migrations. A second Open for the same id
// returns the live Store, re-applying the quota. The plugin id is the only
// thing choosing the path, and it must be one safe segment; there is no way
// to name another plugin's file. Callers should Open on every use rather than
// keep the Store: idle stores are closed (CloseIdle) and capped (16).
func (m *Manager) Open(ctx context.Context, id string, o Options) (*Store, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	// Fast path (every db_exec/db_query goes through here): the store is
	// already open, the quota is unchanged and no migration is asked for.
	// No per-id lock, no PRAGMA, one short m.mu hold. A store closed in the
	// meantime reports ErrClosed from its own acquire.
	m.mu.Lock()
	s := m.open[id]
	m.mu.Unlock()
	if s != nil && o.NoMigrate && s.quota.Load() == effectiveQuota(o.QuotaBytes) &&
		(o.KeepYears <= 0 || s.full.Load()) && !s.closed() {
		s.touch() // about to be used: keep CloseIdle away from it
		if o.BusyWait > 0 {
			s.busyWait.Store(int64(o.BusyWait))
		}
		return s, nil
	}
	unlock, err := m.lockIDCtx(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()

	m.mu.Lock()
	s = m.open[id]
	m.mu.Unlock()
	if s != nil && !s.closed() {
		if o.BusyWait > 0 {
			s.busyWait.Store(int64(o.BusyWait))
		}
		if o.KeepYears > 0 && !s.full.Load() {
			if err := s.raiseSync(ctx); err != nil {
				return nil, err
			}
		}
		if s.quota.Load() != effectiveQuota(o.QuotaBytes) {
			if err := s.requota(ctx, o.QuotaBytes); err != nil {
				return nil, err
			}
		}
		if !o.NoMigrate {
			// Re-validate migrations (an upgrade passes a longer set).
			if err := s.migrate(ctx, o.Migrations, m.now()); err != nil {
				return s, err
			}
		}
		return s, nil
	}
	dir, err := m.safeDir(id, true)
	if err != nil {
		return nil, err
	}
	s, err = openStore(ctx, id, dir, o, m.now())
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.open[id] = s
	victims := m.evictLocked(id)
	m.mu.Unlock()
	closeVictims(victims)
	return s, nil
}

func (s *Store) requota(ctx context.Context, quota int64) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := applyQuota(ctx, s.conn, quota); err != nil {
		return err
	}
	s.quota.Store(effectiveQuota(quota))
	return nil
}

// evictLocked unlinks least-recently-used idle stores while more than
// maxOpenStores are open and returns them; the caller closes them AFTER
// releasing m.mu (closing runs a TRUNCATE checkpoint, an fsync, which must
// not stall every plugin's Open). m.mu is held; keep is never evicted.
func (m *Manager) evictLocked(keep string) []*Store {
	var victims []*Store
	for len(m.open) > maxOpenStores {
		victim, oldest := "", int64(1<<62)
		for id, s := range m.open {
			if id == keep {
				continue
			}
			if t := s.lastUsed.Load(); t < oldest {
				victim, oldest = id, t
			}
		}
		if victim == "" {
			return victims
		}
		s := m.open[victim]
		if !s.claim() {
			return victims // in use: cannot evict it right now; stop rather than spin
		}
		delete(m.open, victim)
		victims = append(victims, s)
	}
	return victims
}

func closeVictims(vs []*Store) {
	for _, s := range vs {
		_ = s.closeClaimed()
	}
}

// CloseIdle closes stores unused for maxIdle (the connection and its file
// descriptors); the data stays and the next Open reopens it. The checkpoint
// on close runs outside the manager lock.
func (m *Manager) CloseIdle(maxIdle time.Duration) {
	cutoff := time.Now().Add(-maxIdle).UnixNano()
	var victims []*Store
	m.mu.Lock()
	for id, s := range m.open {
		if s.lastUsed.Load() <= cutoff && s.claim() {
			delete(m.open, id)
			victims = append(victims, s)
		}
	}
	m.mu.Unlock()
	closeVictims(victims)
}

// Close closes one plugin's Store (unload/disable); the data stays.
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	s := m.open[id]
	delete(m.open, id)
	m.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.Close()
}

// CloseAll closes every open Store (shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	all := m.open
	m.open = map[string]*Store{}
	m.mu.Unlock()
	for _, s := range all {
		_ = s.Close()
	}
}

// removedMarker is written next to a soft-deleted tree.
type removedMarker struct {
	PluginID    string    `json:"plugin_id"`
	RemovedAt   time.Time `json:"removed_at"`
	PurgeAfter  time.Time `json:"purge_after"`
	RetainUntil time.Time `json:"retain_until,omitempty"`
	KeepYears   int       `json:"keep_years,omitempty"`
	ReasonKey   string    `json:"reason_key,omitempty"`
}

func (mk removedMarker) deletableAt() time.Time {
	if mk.RetainUntil.After(mk.PurgeAfter) {
		return mk.RetainUntil
	}
	return mk.PurgeAfter
}

// RemovedInfo describes a plugin's newest soft-deleted data.
type RemovedInfo struct {
	Dir         string
	PurgeAfter  time.Time
	RetainUntil time.Time
	ReasonKey   string // the manifest's retention.reason_key (a locale key)
	Retained    bool   // inside the retention period: may not be deleted
}

// removedRoot is <root>/.removed, refused when it is a symlink.
func (m *Manager) removedRoot(create bool) (string, error) {
	rr, err := m.resolvedRoot()
	if err != nil {
		return "", err
	}
	r := filepath.Join(rr, removedDir)
	ok, err := realDir(r)
	if err != nil {
		return "", err
	}
	if !ok && create {
		if err := os.Mkdir(r, 0o700); err != nil && !os.IsExist(err) {
			return "", err
		}
		if ok, err = realDir(r); err != nil || !ok {
			if err == nil {
				err = ErrSymlink
			}
			return "", err
		}
	}
	return r, nil
}

// UninstallRetained closes the plugin's database and moves its whole data
// tree to .removed/<id>-<YYYYMMDD>. Nothing is deleted: the data is kept for
// SoftDeleteDays (a reinstall restores it) and, when the manifest declares
// retention.keep_years, for that many years with deletion refused meanwhile;
// reasonKey (retention.reason_key) is stored so the UI can say why. It
// touches only plugin-data; the code tree is uninstalled separately.
func (m *Manager) UninstallRetained(id string, keepYears int, reasonKey string) (string, error) {
	if err := validID(id); err != nil {
		return "", err
	}
	if keepYears < 0 {
		return "", fmt.Errorf("plugindb: negative keep_years")
	}
	unlock := m.lockID(id)
	defer unlock()
	if err := m.Close(id); err != nil {
		return "", err
	}
	m.removedMu.Lock()
	defer m.removedMu.Unlock()
	src, err := m.safeDir(id, false)
	if err != nil {
		return "", err
	}
	if ok, err := realDir(src); err != nil {
		return "", err
	} else if !ok {
		return "", nil // plugin never had data
	}
	rdir, err := m.removedRoot(true)
	if err != nil {
		return "", err
	}
	now := m.now().UTC()
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
	mk := removedMarker{PluginID: id, RemovedAt: now, PurgeAfter: now.AddDate(0, 0, SoftDeleteDays), KeepYears: keepYears, ReasonKey: reasonKey}
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
// removedMu must be held.
func (m *Manager) latestRemoved(id string) (string, removedMarker, error) {
	rdir, err := m.removedRoot(false)
	if err != nil {
		return "", removedMarker{}, err
	}
	ents, err := os.ReadDir(rdir)
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
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(name, id+"-") {
			continue
		}
		mk, ok := readMarker(filepath.Join(rdir, name))
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
	return filepath.Join(rdir, best), bestMk, nil
}

// RemovedInfo reports the newest soft-deleted data for id (ErrNotRemoved if none).
func (m *Manager) RemovedInfo(id string) (RemovedInfo, error) {
	if err := validID(id); err != nil {
		return RemovedInfo{}, err
	}
	m.removedMu.Lock()
	defer m.removedMu.Unlock()
	dir, mk, err := m.latestRemoved(id)
	if err != nil {
		return RemovedInfo{}, err
	}
	return RemovedInfo{Dir: dir, PurgeAfter: mk.PurgeAfter, RetainUntil: mk.RetainUntil, ReasonKey: mk.ReasonKey,
		Retained: m.now().Before(mk.RetainUntil)}, nil
}

func readMarker(dir string) (removedMarker, bool) {
	p := filepath.Join(dir, markerName)
	if ok, err := regularOrMissing(p); err != nil || !ok {
		return removedMarker{}, false
	}
	b, err := os.ReadFile(p)
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
	unlock := m.lockID(id)
	defer unlock()
	m.removedMu.Lock()
	defer m.removedMu.Unlock()
	dst, err := m.safeDir(id, false)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return ErrExists
	}
	src, _, err := m.latestRemoved(id)
	if err != nil {
		return err
	}
	// Move first, drop the marker after: a failed move must leave the tree
	// marked, or it could never be restored or purged again.
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dst, markerName))
	return nil
}

// DeleteNow is the operator's explicit "delete data now" for removed data. It
// is refused with ErrRetained while a keep_years period runs (RemovedInfo has
// the reason key). The GB logbook hard delete is the plugin's own typed-confirm
// flow and does not go through here.
func (m *Manager) DeleteNow(id string) error {
	if err := validID(id); err != nil {
		return err
	}
	m.removedMu.Lock()
	defer m.removedMu.Unlock()
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
// are candidates; anything else is left alone. housekeeping calls it daily.
func (m *Manager) Prune() ([]string, error) {
	m.removedMu.Lock()
	defer m.removedMu.Unlock()
	rdir, err := m.removedRoot(false)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(rdir)
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
		dir := filepath.Join(rdir, name)
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
