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
// 067_shift_cash_reconciliation.sql). sort.Slice above has no defined
// tie-break for equal keys, so before this check a collision would let one
// file silently win the sort and the other silently lose — nothing built,
// nothing tested, would say so, and on some installs a table/column would
// simply never get created. A loud boot-time error is strictly better than
// a silent, order-dependent data-loss bug in a fiscal/compliance-relevant
// schema. (scripts/ci/guard-migration-version-collision.sh catches the same
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
