package offsitebackup

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
)

// Service wires the uploader, its scheduler and the restorer for one till.
// server.Start runs it; the Settings pages use the same instance.
type Service struct {
	Uploader  *Uploader
	Scheduler *Scheduler
	Restorer  *Restorer
	Cloud     Cloud
	Settings  Store
	cfg       *config.Config
	sqlDB     *sql.DB
}

var (
	shared   *Service
	sharedMu sync.Mutex
)

// Shared returns the till's one Service, creating it on first use (a call
// with a different database, as tests make, builds a new one).
func Shared(cfg *config.Config, sqlDB *sql.DB) *Service {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil || shared.sqlDB != sqlDB {
		shared = NewService(cfg, sqlDB)
	}
	return shared
}

// NewService builds a Service on the shop's real cloud connection.
func NewService(cfg *config.Config, sqlDB *sql.DB) *Service {
	set := data.NewSettingsRepo(sqlDB)
	cloud := &HTTPCloud{Resolve: ConnFromConfig(cfg), HTTP: newStoreClient()}
	s := &Service{Cloud: cloud, Settings: set, cfg: cfg, sqlDB: sqlDB}
	liveStore := func() string {
		if v, _, err := set.Get(context.Background(), "marketplace.store_id"); err == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return strings.TrimSpace(cfg.Marketplace.StoreID)
	}
	s.Uploader = &Uploader{
		DBPath: cfg.DBPath, Settings: set, Cloud: cloud, AppVersion: buildinfo.Version, StoreID: liveStore,
		Gate: func(ctx context.Context, now time.Time) GateState { return CheckGate(ctx, cfg, set, now) },
	}
	s.Scheduler = &Scheduler{U: s.Uploader}
	s.Restorer = &Restorer{
		DBPath: cfg.DBPath, Cloud: cloud, StoreID: liveStore, LiveStoreID: liveStore,
		Choice: func() string { v, _, _ := set.Get(context.Background(), data.OffsiteKeyInCloudKey); return v },
		OnLinked: func() {
			// Linked: the "needs linking" wait is over, back up now.
			s.Uploader.resume(context.Background())
			s.Scheduler.Trigger()
		},
		BeforeStage: func(context.Context) error {
			// The same safety backup POST /api/backup/restore takes: a
			// snapshot whose photos failed still holds every row.
			path, err := db.SnapshotWithAssets(sqlDB, cfg.DBPath, paths.Data("public", "assets"))
			if path == "" {
				return err
			}
			return nil
		},
	}
	return s
}

// HasKey reports whether this till holds the shop's backup key.
func (s *Service) HasKey() bool {
	_, _, err := s.Uploader.Keys.Load(s.Uploader.storeID())
	return err == nil
}

// init: a till that has been paired with a different shop forgets the old
// shop's backup key (K1), only after the new pairing succeeded. enroll cannot import this package, so
// it calls the hook.
func init() {
	enroll.OnStoreLeft = func(oldStoreID string) { KeyStore{}.DeleteForStore(oldStoreID) }
}

// Gate is the live gate state, for the Settings card.
func (s *Service) Gate(ctx context.Context) GateState {
	return CheckGate(ctx, s.cfg, s.Settings, time.Now())
}

// Run records a finished cloud restore, then runs the scheduler until ctx is
// cancelled. Call it from its own goroutine.
func (s *Service) Run(ctx context.Context) {
	s.recordAppliedRestore(ctx)
	s.Scheduler.Run(ctx)
}

// restoreNote is the small file Restorer leaves next to the database when it
// stages a restore: the database it describes is about to be replaced, so the
// audit row about it is written after the restart, into the restored database.
type restoreNote struct {
	BackupID string `json:"backup_id"`
	TakenAt  string `json:"taken_at"`
	StagedAt string `json:"staged_at"`
}

const noteName = "cloud-restore-note.json"

func notePath(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), noteName) }

func writeNote(dbPath string, n restoreNote) {
	b, _ := json.Marshal(n)
	if err := os.WriteFile(notePath(dbPath), b, 0o600); err != nil {
		logging.L().Warnf("offsitebackup: could not leave the restore note: %v", err)
	}
}

// recordAppliedRestore writes the audit row (AC5.4) once the staged restore
// has been applied by the boot that is now running. A note whose restore is
// still pending waits; one whose restore was refused is dropped.
func (s *Service) recordAppliedRestore(ctx context.Context) {
	raw, err := os.ReadFile(notePath(s.cfg.DBPath))
	if err != nil {
		return
	}
	if db.PendingRestore(s.cfg.DBPath) {
		return // not restarted yet
	}
	defer os.Remove(notePath(s.cfg.DBPath))
	if db.RestoreRefused(s.cfg.DBPath) {
		return
	}
	var n restoreNote
	if json.Unmarshal(raw, &n) != nil {
		return
	}
	_ = data.NewPOSRepo(s.sqlDB).InsertAudit(ctx, nil, "", "backup", "-", "cloud_restore_applied",
		map[string]any{"backup_id": n.BackupID, "taken_at": n.TakenAt}, time.Now().UTC().Format(time.RFC3339), "")
}
