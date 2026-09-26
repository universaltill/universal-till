package pages

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Replica asset prune (ut-docs#2785). The asset pull used to only add and
// replace, so a photo the owner deleted on the main till stayed on every
// replica for good. Now the replica removes a file when all of these hold:
//
//   - the main's manifest for that scope says "complete" (an older main, or
//     a walk that hit an unreadable entry, never causes a prune);
//   - the file is in the replica's ledger — it was downloaded from the main
//     (a photo uploaded on the replica itself, #1689, never is) — and is
//     still exactly as downloaded (size + mtime), so a later local upload
//     over it is left alone;
//   - the manifest has not listed it in at least two consecutive complete
//     manifests AND for at least the grace (1h), so neither a transient gap
//     on the main nor a wall-clock jump alone deletes; a relisting resets
//     both;
//   - the scope passes the safety valve (pruneHeld): the main's complete
//     manifest is not empty while this replica holds downloads, and the
//     files it stopped listing are not more than half of them — a main
//     whose photo folder was lost or restored empty must never make every
//     replica delete what may be the last copies;
//   - no basket (cashier, kiosk, table QR) has lines — the prune waits for
//     the sale to end;
//   - it resolves to a regular file inside the scope's asset root (no
//     traversal, no symlink, no symlinked parent) — one os.Remove, never a
//     RemoveAll or glob.
//
// At most syncAssetPruneMaxPerTick files go per tick. Only the replica pull
// tick calls this (it needs sync.primary_url), so a main or standalone till
// never prunes by this path.

// syncAssetPruneMaxPerTick bounds the deletions one pull tick makes.
const syncAssetPruneMaxPerTick = 50

// syncAssetPruneGrace is how long the main must have stopped listing a file.
const syncAssetPruneGrace = time.Hour

// syncAssetPruneMinMisses is how many consecutive complete manifests must
// miss a file before it goes, whatever the wall clock says.
const syncAssetPruneMinMisses = 2

// Safety valve: a scope's prune is held when the files the main stopped
// listing are more than syncAssetPruneHoldRatio of its ledger and more than
// syncAssetPruneHoldMin of them (or when the main lists nothing at all).
const (
	syncAssetPruneHoldRatio = 0.5
	syncAssetPruneHoldMin   = 5
)

// assetPruneHeld remembers which scopes' prunes are held, so the warning is
// logged once per condition rather than on every pull tick; the pruner is
// rebuilt each tick, so this lives at package level.
var (
	assetPruneHeldMu sync.Mutex
	assetPruneHeld   = map[string]bool{}
)

func assetPruneHeldKey(scope string) string { return "sync.asset_prune_held." + scope }

type assetPruner struct {
	ledger     *data.SyncAssetLedgerRepo
	busy       func() bool
	now        func() time.Time
	grace      time.Duration
	maxPerTick int
}

// newAssetPruner wires the production pruner for the replica pull tick.
func newAssetPruner(d *common.Deps) *assetPruner {
	if d == nil || d.Db == nil {
		return nil
	}
	return &assetPruner{
		ledger:     data.NewSyncAssetLedgerRepo(d.Db),
		busy:       func() bool { return anyBasketHasItems(d) },
		now:        time.Now,
		grace:      syncAssetPruneGrace,
		maxPerTick: syncAssetPruneMaxPerTick,
	}
}

// anyBasketHasItems reports whether a sale is in progress on this till:
// the cashier's basket, the kiosk's, or any table-QR guest session's.
func anyBasketHasItems(d *common.Deps) bool {
	return (d.Engine != nil && d.Engine.HasItems()) ||
		(d.KioskEngine != nil && d.KioskEngine.HasItems()) ||
		d.SelfOrderSessions.HasItems()
}

// pruneTick is one pull tick's prune state, shared across scopes so the
// batch bound is per tick, not per scope. batch collects the current
// scope's ledger changes; flush writes them in one transaction.
type pruneTick struct {
	p      *assetPruner
	now    time.Time
	busy   bool
	budget int
	pruned int
	batch  data.SyncAssetLedgerBatch
}

func (p *assetPruner) tick() *pruneTick {
	return &pruneTick{
		p:      p,
		now:    p.now(),
		busy:   p.busy != nil && p.busy(),
		budget: p.maxPerTick,
	}
}

// flush writes the current scope's pending ledger changes. Nil-safe.
func (t *pruneTick) flush(ctx context.Context, s assetScope) {
	if t == nil {
		return
	}
	b := t.batch
	t.batch = data.SyncAssetLedgerBatch{}
	if err := t.p.ledger.Apply(ctx, s.dir, b); err != nil {
		logging.L().Errorf("sync pull: %v", err)
	}
}

// record notes that local now holds the main's copy of rel. Nil-safe: a
// pull without a pruner records nothing. Queues a write only when the row
// changes (flush writes it).
func (t *pruneTick) record(ctx context.Context, s assetScope, ledger map[string]data.SyncAssetLedgerRow, rel, local string) {
	if t == nil {
		return
	}
	st, err := os.Lstat(local)
	if err != nil || !st.Mode().IsRegular() {
		return
	}
	size, mod := st.Size(), st.ModTime().Unix()
	if row, ok := ledger[rel]; ok && row.Size == size && row.Mod == mod && row.UnreferencedSince == 0 && row.Misses == 0 {
		return
	}
	row := data.SyncAssetLedgerRow{Path: rel, Size: size, Mod: mod}
	t.batch.Record = append(t.batch.Record, row)
	ledger[rel] = row
}

// pruneHeld is the safety valve: it reports (and warns, once per
// condition) when the main's complete manifest would unreference an
// implausible share of this scope's downloads.
func pruneHeld(s assetScope, ledgerRows, unreferenced, listed int) bool {
	held := ledgerRows > 0 && (listed == 0 ||
		(unreferenced > syncAssetPruneHoldMin && float64(unreferenced) > syncAssetPruneHoldRatio*float64(ledgerRows)))
	key := assetPruneHeldKey(s.dir)
	assetPruneHeldMu.Lock()
	was := assetPruneHeld[s.dir]
	assetPruneHeld[s.dir] = held
	assetPruneHeldMu.Unlock()
	switch {
	case held && !was:
		logging.L().WarnProblemf(key,
			"sync pull: %s image prune held — the main till's list would remove %d of %d photo(s) this till downloaded (the main lists %d); check the main till's photo folder was not lost or restored empty. Nothing is deleted until the main lists them again or the difference is small",
			s.dir, unreferenced, ledgerRows, listed)
	case !held && was:
		if logging.ResolveProblems(key) > 0 {
			logging.L().Infof("sync pull: %s image prune resumed", s.dir)
		}
	}
	return held
}

// prune applies the rules above to one scope against its complete manifest.
func (t *pruneTick) prune(ctx context.Context, s assetScope, ledger map[string]data.SyncAssetLedgerRow, manifest []assetEntry) {
	if t == nil {
		return
	}
	live := make(map[string]bool, len(manifest))
	for _, e := range manifest {
		live[e.Path] = true
	}
	keys := make([]string, 0, len(ledger))
	unreferenced := 0
	for k := range ledger {
		keys = append(keys, k)
		if !live[k] {
			unreferenced++
		}
	}
	if pruneHeld(s, len(ledger), unreferenced, len(manifest)) {
		return // leave the ledger untouched while held
	}
	sort.Strings(keys)
	t.batch.MissAt = t.now.Unix()
	for _, rel := range keys {
		row := ledger[rel]
		if live[rel] {
			if row.UnreferencedSince != 0 || row.Misses != 0 { // listed again: misses and grace restart
				t.batch.Record = append(t.batch.Record, data.SyncAssetLedgerRow{Path: rel, Size: row.Size, Mod: row.Mod})
			}
			continue
		}
		since := row.UnreferencedSince
		if since == 0 {
			since = t.now.Unix()
		}
		misses := row.Misses + 1 // this manifest missed it too
		if misses < syncAssetPruneMinMisses || t.now.Sub(time.Unix(since, 0)) < t.p.grace || t.busy || t.budget <= 0 {
			t.batch.Miss = append(t.batch.Miss, rel)
			continue
		}
		removed, forget := s.removeDownloaded(rel, row)
		if removed {
			t.budget--
			t.pruned++
		}
		if forget {
			t.batch.Delete = append(t.batch.Delete, rel)
		} else {
			t.batch.Miss = append(t.batch.Miss, rel)
		}
	}
}

// removeDownloaded deletes one ledgered file if it is still safely ours.
// forget reports that the ledger row should go: the file was removed, is
// already gone, or is not something this prune may ever touch.
func (s assetScope) removeDownloaded(rel string, row data.SyncAssetLedgerRow) (removed, forget bool) {
	local, ok := s.safePath(rel)
	if !ok || !s.contains(local) {
		return false, true
	}
	st, err := os.Lstat(local)
	if os.IsNotExist(err) {
		return false, true
	}
	if err != nil {
		return false, false
	}
	if !st.Mode().IsRegular() || st.Size() != row.Size || st.ModTime().Unix() != row.Mod {
		return false, true // a symlink, or replaced locally since the download
	}
	if err := os.Remove(local); err != nil {
		logging.L().Errorf("sync pull: remove stale %s image: %v", s.dir, err)
		return false, false
	}
	s.removeEmptyParents(filepath.Dir(local))
	return true, true
}

// contains reports whether local's directory really resolves inside the
// scope root — a symlinked parent directory pointing elsewhere fails.
func (s assetScope) contains(local string) bool {
	realRoot, err := filepath.EvalSymlinks(s.root())
	if err != nil {
		return false
	}
	realDir, err := filepath.EvalSymlinks(filepath.Dir(local))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(realRoot, realDir)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// removeEmptyParents removes now-empty directories from dir up to, but
// never including, the scope root. os.Remove refuses a non-empty directory,
// so the first one still holding anything stops the walk.
func (s assetScope) removeEmptyParents(dir string) {
	root := filepath.Clean(s.root())
	for {
		dir = filepath.Clean(dir)
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return
		}
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
