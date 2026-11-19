package data

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNotReplica: a write that only a replica may make was refused because
// this till has no sync.primary_url, i.e. it is not (or, after a promote that
// won the race, no longer) a replica. Nothing was written (ut-docs#5896,
// ut-docs#5903).
var ErrNotReplica = errors.New("this till is not a replica (no sync.primary_url)")

// requireReplica returns ErrNotReplica unless sync.primary_url is set,
// reading it through q (pass the open tx so the check is serialised against a
// concurrent ClearReplicaIdentity: the DB uses _txlock=immediate).
func requireReplica(ctx context.Context, q rowQuerier) error {
	primaryURL, _, err := AgentSettingRaw(ctx, q, "sync.primary_url")
	if err != nil {
		return fmt.Errorf("read sync.primary_url: %w", err)
	}
	if strings.TrimSpace(primaryURL) == "" {
		return ErrNotReplica
	}
	return nil
}

// IsReplica reports whether this till currently has a sync.primary_url
// (uncached read). For callers that cannot put the check inside a
// transaction (the plugin converge, ut-docs#5903).
func (r *SettingsRepo) IsReplica(ctx context.Context) (bool, error) {
	switch err := requireReplica(ctx, r.db); {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotReplica):
		return false, nil
	default:
		return false, err
	}
}
