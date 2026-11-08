package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrNotReplica: a replica-only write was refused because this till has no
// sync.primary_url, i.e. it is not (or, after a promote that won the race,
// no longer) a replica. Nothing was written (ut-docs#5896, #5903).
var ErrNotReplica = errors.New("this till is not a replica (no sync.primary_url)")

// requireReplica is the in-transaction replica check every pull-tick write
// shares (ut-docs#5896, #5903). Call it as the FIRST statement of the write's
// tx: the DB uses _txlock=immediate, so the tx holds the write lock from BEGIN
// and is serialised against ClearReplicaIdentity (promote). Either promote
// committed first (blank: refuse) or this tx commits first (promote then
// deletes / overwrites what it wrote).
func requireReplica(ctx context.Context, tx *sql.Tx) error {
	v, _, err := AgentSettingRaw(ctx, tx, "sync.primary_url")
	if err != nil {
		return fmt.Errorf("read sync.primary_url: %w", err)
	}
	if strings.TrimSpace(v) == "" {
		return ErrNotReplica
	}
	return nil
}
