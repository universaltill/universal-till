// Package plugindb is the host side of ADR-0121 §4 (build 5, slice A): one
// SQLite file per plugin under <data>/plugin-data/<id>/plugin.db, hardened per
// connection, with checksum-pinned plugin-owned migrations and an
// uninstall/retention lifecycle that never silently deletes shop data.
//
// It lives under internal/db because it owns SQL text (the migration ledger);
// plugins reach it only through Store.Exec / Store.Query, which are the Go
// shape of the future db_exec / db_query host functions.
//
// Isolation is by file, not by parsing SQL: a Store has exactly one database
// file, ATTACH is disabled with SQLITE_LIMIT_ATTACHED=0, and modernc SQLite
// ships no loadable-extension support (load_extension is an error), which a
// test pins so a driver upgrade that changes it fails CI.
package plugindb

// Slice B (ut-docs#3157) adds, still without feature code: BLOB base64 args
// and results, a semaphore with ErrBusy, per-id locks, NoMigrate, idle close
// and an LRU cap, symlink refusal, statement limits, SnapshotFile (VACUUM
// INTO), install/upgrade migration with a .pre-upgrade copy, rollback restore,
// retention reason keys, and a read-only immutable ImportDB.
