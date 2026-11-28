package plugin

import (
	"encoding/json"
	"fmt"
)

// Plugin-records channel (ut-docs#3697, ADR-0149 §3; permission
// "records:sync" and a manifest records_forward block naming the
// collections and snapshots). All five calls count toward one cap of 64
// records_*/snapshot_* calls per event (ErrQuota past it).

// recordsStatusMax is the buffer records_status needs at most (256 KiB).
const recordsStatusMax = 256 << 10

// RecordsForward queues one record for the main till. queued is true when
// this till is a replica and the record is in its outbox (the same
// collection and id again is a no-op that also reports true); false means
// this till is not a replica (the main or a standalone till), nothing was
// queued and that is not an error — the plugin keeps its own row. The plugin
// id is the calling module's, never an argument. payload must marshal to a
// JSON object of at most 64 KiB (ErrInvalid otherwise); ErrDenied: no
// records:sync or a collection outside records_forward.collections.
// It never waits for the network.
func RecordsForward(collection, id string, payload any) (queued bool, err error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	n := rawRecordsForward([]byte(collection), []byte(id), p)
	if n < 0 {
		return false, codeErr("records_forward", n)
	}
	return n == 0, nil
}

// RecordsRole answers "replica" when this till has joined a main till
// (satellites included), else "main".
func RecordsRole() (string, error) {
	out, err := bufCall("records_role", 32, rawRecordsRole)
	return string(out), err
}

// RecordStatus is one of this plugin's records still in the outbox.
// Status is "pending" or "rejected"; Reason says why a rejected one was.
type RecordStatus struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	Attempts int    `json:"attempts"`
}

// RecordsStatus lists this plugin's outbox rows for a collection, at most
// 200: rejected rows first, then the rest, newest first within each group. A
// delivered row is gone, so absence means delivered or never forwarded.
func RecordsStatus(collection string) ([]RecordStatus, error) {
	c := []byte(collection)
	out, err := bufCall("records_status", recordsStatusMax, func(dst []byte) int32 { return rawRecordsStatus(c, dst) })
	if err != nil {
		return nil, err
	}
	var rows []RecordStatus
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("ut records_status: decode: %w", err)
	}
	return rows, nil
}

// SnapshotSet publishes v as the plugin's snapshot for a collection in
// records_forward.snapshots; every till can read it with SnapshotGet after
// the next admin pull. stored is false on a replica (the main till owns
// snapshots; the call is ignored, not an error). v must marshal to JSON of
// at most 64 KiB (ErrInvalid otherwise).
func SnapshotSet(collection string, v any) (stored bool, err error) {
	p, err := json.Marshal(v)
	if err != nil {
		return false, err
	}
	n := rawSnapshotSet([]byte(collection), p)
	if n < 0 {
		return false, codeErr("snapshot_set", n)
	}
	return n == 0, nil
}

// SnapshotGet reads the plugin's own snapshot for a collection into out.
// found is false (and err nil) when none is stored on this till.
func SnapshotGet(collection string, out any) (found bool, err error) {
	c := []byte(collection)
	raw, err := bufCall("snapshot_get", defaultBuf, func(dst []byte) int32 { return rawSnapshotGet(c, dst) })
	if err != nil {
		if e, ok := err.(*Error); ok && e.Code == ErrNotFound.Code {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return false, fmt.Errorf("ut snapshot_get: decode: %w", err)
	}
	return true, nil
}
