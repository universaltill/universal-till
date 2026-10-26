package db

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
)

// A backup file is untrusted input (a USB stick, another computer): its
// schema can carry triggers, views or virtual tables no till ever made.
// A trigger on settings could re-insert a deleted credential or overwrite
// the live till's (ut-docs#5070 review), so a file is only kept or applied
// when its active schema objects are ones this build's own migrations
// create.

// schemaObject is one trigger, view or table in sqlite_schema.
type schemaObject struct{ typ, name, tbl, sql string }

var (
	referenceSchemaMu  sync.Mutex
	referenceSchemaMap map[string]schemaObject // "type\x00name" → object
)

// referenceSchema is the schema a freshly migrated database has, built
// once per process in a throwaway file in dir (named like an upload part,
// so a crash leaves nothing the boot sweep does not remove).
func referenceSchema(dir string) (map[string]schemaObject, error) {
	referenceSchemaMu.Lock()
	defer referenceSchemaMu.Unlock()
	if referenceSchemaMap != nil {
		return referenceSchemaMap, nil
	}
	f, err := os.CreateTemp(dir, uploadPartPattern)
	if err != nil {
		return nil, fmt.Errorf("reference schema: %w", err)
	}
	p := f.Name()
	_ = f.Close()
	_ = os.Remove(p) // Open creates it fresh
	defer func() {
		for _, s := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(p + s)
		}
	}()
	ref, err := Open(p)
	if err != nil {
		return nil, fmt.Errorf("reference schema: %w", err)
	}
	objs, err := schemaObjects(ref.DB)
	_ = ref.Close()
	if err != nil {
		return nil, err
	}
	m := make(map[string]schemaObject, len(objs))
	for _, o := range objs {
		m[o.typ+"\x00"+o.name] = o
	}
	referenceSchemaMap = m
	return m, nil
}

func schemaObjects(d *sql.DB) ([]schemaObject, error) {
	rows, err := d.Query(`SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_schema WHERE type IN ('table', 'view', 'trigger')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []schemaObject
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.typ, &o.name, &o.tbl, &o.sql); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// checkMigratedSchema refuses a database (already migrated to this build)
// holding any trigger or view that is not, byte for byte, one this build
// creates, any virtual table, or any table this build does not have
// (except the backup's own photo table).
func checkMigratedSchema(d *sql.DB, dir string) error {
	ref, err := referenceSchema(dir)
	if err != nil {
		return err
	}
	objs, err := schemaObjects(d)
	if err != nil {
		return err
	}
	for _, o := range objs {
		if strings.HasPrefix(o.name, "sqlite_") {
			continue
		}
		if o.typ == "table" && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(o.sql)), "CREATE VIRTUAL") {
			return fmt.Errorf("%w: virtual table %s", ErrNotTillBackup, o.name)
		}
		if o.typ == "table" && o.name == backupAssetsTable {
			continue
		}
		r, ok := ref[o.typ+"\x00"+o.name]
		if !ok {
			return fmt.Errorf("%w: unknown %s %s", ErrNotTillBackup, o.typ, o.name)
		}
		if o.typ != "table" && r.sql != o.sql {
			return fmt.Errorf("%w: %s %s differs from this build's", ErrNotTillBackup, o.typ, o.name)
		}
	}
	return nil
}

// checkSettingsTriggers is the boot-time check before a staged restore's
// settings are rewritten (the file may be an older version's, so only the
// triggers that act on the rewrite are checked): every trigger on settings
// must be one this build creates, unchanged.
func checkSettingsTriggers(d *sql.DB, dir string) error {
	ref, err := referenceSchema(dir)
	if err != nil {
		return err
	}
	objs, err := schemaObjects(d)
	if err != nil {
		return err
	}
	for _, o := range objs {
		if o.typ != "trigger" || !strings.EqualFold(o.tbl, "settings") {
			continue
		}
		if r, ok := ref["trigger\x00"+o.name]; !ok || r.sql != o.sql {
			return fmt.Errorf("unknown trigger %s on settings", o.name)
		}
	}
	return nil
}
