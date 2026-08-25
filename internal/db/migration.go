package db

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migration struct {
	Version int
	Name    string
	SQL     string
}

func loadMigrations() ([]migration, error) {
	return loadMigrationsFromFS(migrationsFS, "migrations")
}

// loadMigrationsFromFS does the real work of loadMigrations against an
// injected fs.FS, so tests can exercise the full load→dedup→sort path
// (including checkNoDuplicateVersions actually being wired in) against a
// fixture directory without touching the real embedded migrations — a
// fake collision can't be added to migrationsFS itself since //go:embed
// only ever sees the real on-disk files.
func loadMigrationsFromFS(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	var migs []migration

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()

		parts := strings.SplitN(name, "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid migration name: %s", name)
		}

		v, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("invalid migration version %s: %w", name, err)
		}

		b, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}

		migs = append(migs, migration{
			Version: v,
			Name:    name,
			SQL:     string(b),
		})
	}

	if err := checkNoDuplicateVersions(migs); err != nil {
		return nil, err
	}

	sort.Slice(migs, func(i, j int) bool {
		return migs[i].Version < migs[j].Version
	})

	return migs, nil
}

// checkNoDuplicateVersions hard-fails when two migration files share the
// same leading Version number (ut-docs#1056, found live: two concurrent
// pipeline lanes independently created 067_vouchers.sql and
// 067_shift_cash_reconciliation.sql). This is NOT a sort-order/tie-break
// problem (correction, follow-up independent review — an earlier version
// of this comment claimed it was, which was wrong): db.go's migrate()
// reads the highest applied version ONCE via `SELECT MAX(version)` before
// its loop, then skips any migration whose Version is <= that watermark —
// never re-checking per file. Two concrete failure modes, neither about
// ordering:
//  1. Fresh install (watermark=0): BOTH colliding files pass the
//     "newer than watermark" check and try to apply. The first succeeds
//     and INSERTs its version into schema_migrations (version is its
//     PRIMARY KEY); the second's own INSERT then hits that constraint and
//     fails — a loud boot error, not silent, but still wrong (one
//     migration's DDL ran, the other's didn't, on the same version).
//  2. Upgrade from an install that already has ONE of the two files
//     (watermark already == 67, say): a later release adds the SECOND
//     067-numbered file. Its Version <= watermark is true too, so it is
//     skipped ENTIRELY, forever — the watermark already passed 67 and
//     never looks backward. This is the real silent-data-loss case: a
//     table/column simply never gets created on upgrade, no error at all.
//
// A loud boot-time error here turns case 2 into case 1's kind of failure
// (loud, not silent) whenever it's caught before both versions of the
// binary ship — strictly better than the alternative even though it
// can't undo an already-shipped release's silent skip.
// (scripts/ci/guard-migration-version-collision.sh catches the same
// collision earlier, at PR time, without needing a Go build — this check
// is the belt to that guard's braces: it can never be bypassed by skipping
// a script, because it's the loader every till actually runs.)
func checkNoDuplicateVersions(migs []migration) error {
	seen := make(map[int]string, len(migs))
	for _, m := range migs {
		if prev, ok := seen[m.Version]; ok {
			return fmt.Errorf("duplicate migration version %d: %s collides with %s — rename one (ut-docs#1056)",
				m.Version, m.Name, prev)
		}
		seen[m.Version] = m.Name
	}
	return nil
}
