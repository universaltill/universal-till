//go:build !wasip1

package plugin

import (
	"encoding/json"
	"regexp"
	"slices"
)

// FakeRecord is one row of FakeHost.Outbox.
type FakeRecord struct {
	Collection, ID string
	Payload        json.RawMessage
	Status         string // "pending" (default) or "rejected"
	Reason         string
	Attempts       int
}

// The host's plugin-records bounds the fake enforces (internal/data).
const (
	recordPayloadMax   = 64 << 10
	recordCallsMax     = 64
	recordStatusRows   = 200
	snapshotPayloadMax = 64 << 10
)

var (
	recordCollectionRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	recordIDRe         = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

// recordsEnter counts one records_*/snapshot_* call against the per-event
// cap shared by all five functions.
func recordsEnter(op string) (h *FakeHost, code int32) {
	h, denied := enter(op)
	total := 0
	for _, f := range []string{"records_forward", "records_role", "records_status", "snapshot_set", "snapshot_get"} {
		total += h.Calls[f]
	}
	switch {
	case total > recordCallsMax:
		return h, ErrQuota.Code
	case denied:
		return h, ErrDenied.Code
	}
	return h, 0
}

func rawRecordsForward(col, id, payload []byte) int32 {
	h, code := recordsEnter("records_forward")
	defer h.mu.Unlock()
	if code != 0 {
		return code
	}
	var probe map[string]json.RawMessage
	if len(col) == 0 || len(col) > 32 || len(id) == 0 || len(id) > 64 || len(payload) == 0 || len(payload) > recordPayloadMax ||
		!recordCollectionRe.Match(col) || !recordIDRe.Match(id) || json.Unmarshal(payload, &probe) != nil || probe == nil {
		return ErrInvalid.Code
	}
	if !slices.Contains(h.RecordCollections, string(col)) {
		return ErrDenied.Code
	}
	if !h.Replica {
		return 1
	}
	for _, r := range h.Outbox {
		if r.Collection == string(col) && r.ID == string(id) {
			return 0
		}
	}
	h.Outbox = append(h.Outbox, FakeRecord{Collection: string(col), ID: string(id), Payload: slices.Clone(payload), Status: "pending"})
	return 0
}

func rawRecordsRole(dst []byte) int32 {
	h, code := recordsEnter("records_role")
	defer h.mu.Unlock()
	if code != 0 {
		return code
	}
	role := "main"
	if h.Replica {
		role = "replica"
	}
	return fill(dst, []byte(role))
}

func rawRecordsStatus(col, dst []byte) int32 {
	h, code := recordsEnter("records_status")
	defer h.mu.Unlock()
	if code != 0 {
		return code
	}
	if len(col) == 0 || len(col) > 32 {
		return ErrInvalid.Code
	}
	if !slices.Contains(h.RecordCollections, string(col)) {
		return ErrDenied.Code
	}
	rows := []RecordStatus{}
	for _, r := range h.Outbox {
		if r.Collection != string(col) || len(rows) == recordStatusRows {
			continue
		}
		st := r.Status
		if st == "" {
			st = "pending"
		}
		rows = append(rows, RecordStatus{ID: r.ID, Status: st, Reason: r.Reason, Attempts: r.Attempts})
	}
	out, _ := json.Marshal(rows)
	return fill(dst, out)
}

func rawSnapshotSet(col, payload []byte) int32 {
	h, code := recordsEnter("snapshot_set")
	defer h.mu.Unlock()
	if code != 0 {
		return code
	}
	if len(col) == 0 || len(col) > 32 || len(payload) == 0 || len(payload) > snapshotPayloadMax ||
		!recordCollectionRe.Match(col) || !json.Valid(payload) {
		return ErrInvalid.Code
	}
	if !slices.Contains(h.SnapshotCollections, string(col)) {
		return ErrDenied.Code
	}
	if h.Replica {
		return 1
	}
	h.Snapshots[string(col)] = slices.Clone(payload)
	return 0
}

func rawSnapshotGet(col, dst []byte) int32 {
	h, code := recordsEnter("snapshot_get")
	defer h.mu.Unlock()
	if code != 0 {
		return code
	}
	if len(col) == 0 || len(col) > 32 {
		return ErrInvalid.Code
	}
	if !slices.Contains(h.SnapshotCollections, string(col)) {
		return ErrDenied.Code
	}
	v, ok := h.Snapshots[string(col)]
	if !ok {
		return ErrNotFound.Code
	}
	return fill(dst, v)
}
