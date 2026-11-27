package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Plugin-records host functions (ut-docs#3697, ADR-0149 §3, contract
// reference/contracts/plugin-records.md): records_forward, records_role,
// records_status, snapshot_set, snapshot_get. All are gated by the
// records:sync permission and by the collections the plugin's own signed
// manifest declares in records_forward; the plugin id is always the calling
// module's, never an argument. Nothing here touches the network: a
// forwarded record only lands in the local outbox for the sync loop.

const (
	// RecordsSyncPermission lets a plugin share its records between tills.
	RecordsSyncPermission = "records:sync"
	// RecordsReceivedEvent is the ask the main till delivers a record with.
	RecordsReceivedEvent = "plugin.records.received.ask"
	// recordCallsPerEvent caps records_*/snapshot_* calls in one event.
	recordCallsPerEvent = 64
	// recordStatusCap bounds records_status's JSON answer.
	recordStatusCap = 256 << 10
	// RecordsRoleReplica / RecordsRoleMain are records_role's answers.
	recordsRoleReplica = "replica"
	recordsRoleMain    = "main"
)

// recordsGate runs the checks every records_* call shares: a host state, the
// per-event call cap (-5), the records:sync grant (-2, audited). On success
// the caller gets the state and the plugin's declared records_forward.
func recordsGate(ctx context.Context) (*hostState, *ManifestRecordsForward, int32) {
	s, ok := stateFrom(ctx)
	if !ok {
		return nil, nil, stateErr(ctx)
	}
	s.recordCalls++
	if s.recordCalls > recordCallsPerEvent {
		return nil, nil, hostErrQuota
	}
	if err := CheckPermission(ctx, s.db, s.pluginID, RecordsSyncPermission); err != nil {
		return nil, nil, hostErrDenied
	}
	if !loadManifestFacts(ctx, s) {
		return nil, nil, hostErrDenied // no readable installed manifest: fail closed
	}
	return s, s.recordsForward, 0
}

func recordsDeclared(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}

// deny audits a refusal of a records call and returns the denied code.
func recordsDeny(ctx context.Context, s *hostState, why string) int32 {
	if err := auditPermissionDenial(ctx, s.db, s.pluginID, RecordsSyncPermission, why); err != nil {
		logging.L().Warnf("failed to audit records denial: %v", err)
	}
	return hostErrDenied
}

// isReplica reads sync.primary_url; a read error is reported as an error so
// the caller answers -3 rather than guessing a role.
func isReplica(ctx context.Context, s *hostState) (bool, error) {
	return data.NewSettingsRepo(s.db).IsReplica(ctx)
}

// hostRecordsForward queues one record for the main till. Results: 0
// queued (this till is a replica), 1 not a replica (nothing queued, not an
// error), -2 no records:sync or a collection outside the plugin's own
// records_forward.collections (audited), -3 internal, -4 bad input (name or
// id shape, payload not a JSON object or over 64 KiB), -5 over the
// per-event call cap. The same (plugin, collection, id) again is 0 with no
// second row. A full outbox rejects the OLDEST pending rows (audited), it
// never grows and never fails the call.
func hostRecordsForward(ctx context.Context, m api.Module, colPtr, colLen, idPtr, idLen, payPtr, payLen uint32) int32 {
	s, rf, code := recordsGate(ctx)
	if code != 0 {
		return code
	}
	if colLen == 0 || colLen > 32 || idLen == 0 || idLen > 64 || payLen == 0 || payLen > data.RecordMaxPayload {
		return hostErrInvalid
	}
	colB, cok := readGuest(m, colPtr, colLen)
	idB, iok := readGuest(m, idPtr, idLen)
	payB, pok := readGuest(m, payPtr, payLen)
	if !cok || !iok || !pok {
		return hostErrInvalid
	}
	collection, id := string(colB), string(idB)
	if !data.RecordCollectionRe.MatchString(collection) || !data.RecordIDRe.MatchString(id) || !data.IsJSONObject(payB) {
		return hostErrInvalid
	}
	if rf == nil || !recordsDeclared(rf.Collections, collection) {
		return recordsDeny(ctx, s, "collection "+collection+" not in manifest records_forward.collections")
	}
	replica, err := isReplica(ctx, s)
	if err != nil {
		return hostErrInternal
	}
	if !replica {
		return 1
	}
	over, err := data.NewPluginRecordsRepo(s.db).Enqueue(ctx, s.pluginID, collection, id, bytes.Clone(payB), time.Now().UTC())
	if err != nil {
		logging.L().Warnf("[wasm:%s] records_forward: %v", s.pluginID, err)
		return hostErrInternal
	}
	if over > 0 {
		now := time.Now().UTC().Format(time.RFC3339)
		if err := data.NewPOSRepo(s.db).InsertAudit(ctx, nil, "system", "plugin", s.pluginID, "plugin_record_overflow",
			map[string]any{"collection": collection, "rejected": over}, now, ""); err != nil {
			logging.L().Warnf("[wasm:%s] audit plugin_record_overflow: %v", s.pluginID, err)
		}
	}
	return 0
}

// hostRecordsRole answers "replica" when sync.primary_url is set (a joined
// till, satellites included), else "main", as plain bytes.
func hostRecordsRole(ctx context.Context, m api.Module, dstPtr, dstCap uint32) int32 {
	s, _, code := recordsGate(ctx)
	if code != 0 {
		return code
	}
	replica, err := isReplica(ctx, s)
	if err != nil {
		return hostErrInternal
	}
	role := recordsRoleMain
	if replica {
		role = recordsRoleReplica
	}
	return writeGuest(m, dstPtr, dstCap, []byte(role))
}

// hostRecordsStatus lists this plugin's outbox rows for a collection as a
// JSON array [{id,status,reason,attempts}], oldest first, at most 200 rows
// and 256 KiB. Delivered rows are gone, so absence means delivered or never
// forwarded.
func hostRecordsStatus(ctx context.Context, m api.Module, colPtr, colLen, dstPtr, dstCap uint32) int32 {
	s, rf, code := recordsGate(ctx)
	if code != 0 {
		return code
	}
	if colLen == 0 || colLen > 32 {
		return hostErrInvalid
	}
	colB, ok := readGuest(m, colPtr, colLen)
	if !ok {
		return hostErrInvalid
	}
	collection := string(colB)
	if rf == nil || !recordsDeclared(rf.Collections, collection) {
		return recordsDeny(ctx, s, "collection "+collection+" not in manifest records_forward.collections")
	}
	rows, err := data.NewPluginRecordsRepo(s.db).Status(ctx, s.pluginID, collection)
	if err != nil {
		return hostErrInternal
	}
	for {
		out, err := json.Marshal(rows)
		if err != nil {
			return hostErrInternal
		}
		if len(out) <= recordStatusCap || len(rows) == 0 {
			return writeGuest(m, dstPtr, dstCap, out)
		}
		rows = rows[:len(rows)/2]
	}
}

// hostSnapshotSet stores the plugin's snapshot value for a collection in
// records_forward.snapshots. 0 stored (the version moves only if the bytes
// changed), 1 this till is a replica (ignored: the primary owns snapshots),
// -2 collection not declared, -4 not JSON or over 64 KiB, -5 collection cap.
func hostSnapshotSet(ctx context.Context, m api.Module, colPtr, colLen, payPtr, payLen uint32) int32 {
	s, rf, code := recordsGate(ctx)
	if code != 0 {
		return code
	}
	if colLen == 0 || colLen > 32 || payLen == 0 || payLen > data.SnapshotMaxBytes {
		return hostErrInvalid
	}
	colB, cok := readGuest(m, colPtr, colLen)
	payB, pok := readGuest(m, payPtr, payLen)
	if !cok || !pok {
		return hostErrInvalid
	}
	collection := string(colB)
	if !data.RecordCollectionRe.MatchString(collection) || !json.Valid(payB) {
		return hostErrInvalid
	}
	if rf == nil || !recordsDeclared(rf.Snapshots, collection) {
		return recordsDeny(ctx, s, "snapshot "+collection+" not in manifest records_forward.snapshots")
	}
	replica, err := isReplica(ctx, s)
	if err != nil {
		return hostErrInternal
	}
	if replica {
		return 1
	}
	_, err = data.NewPluginRecordsRepo(s.db).SnapshotSet(ctx, s.pluginID, collection, bytes.Clone(payB), time.Now().UTC())
	switch {
	case err == nil:
		return 0
	case errors.Is(err, data.ErrRecordTooLarge), errors.Is(err, data.ErrRecordInvalid):
		return hostErrInvalid
	case errors.Is(err, data.ErrRecordQuota):
		return hostErrQuota
	}
	logging.L().Warnf("[wasm:%s] snapshot_set: %v", s.pluginID, err)
	return hostErrInternal
}

// hostSnapshotGet returns the stored snapshot JSON of this plugin's own
// namespace on any till; -1 when none is set, -2 when the collection is not
// in the plugin's records_forward.snapshots.
func hostSnapshotGet(ctx context.Context, m api.Module, colPtr, colLen, dstPtr, dstCap uint32) int32 {
	s, rf, code := recordsGate(ctx)
	if code != 0 {
		return code
	}
	if colLen == 0 || colLen > 32 {
		return hostErrInvalid
	}
	colB, ok := readGuest(m, colPtr, colLen)
	if !ok {
		return hostErrInvalid
	}
	collection := string(colB)
	if rf == nil || !recordsDeclared(rf.Snapshots, collection) {
		return recordsDeny(ctx, s, "snapshot "+collection+" not in manifest records_forward.snapshots")
	}
	val, found, err := data.NewPluginRecordsRepo(s.db).SnapshotGet(ctx, s.pluginID, collection)
	if err != nil {
		return hostErrInternal
	}
	if !found {
		return hostErrNotFound
	}
	return writeGuest(m, dstPtr, dstCap, val)
}
