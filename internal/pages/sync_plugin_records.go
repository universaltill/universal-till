package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// LAN sync: the plugin-records channel (ut-docs#3697, ADR-0149 §3, contract
// reference/contracts/plugin-records.md). A plugin on a replica queues
// records with records_forward; the replica's push tick sends them here, to
// the main till, which hands each one to the SAME plugin's
// plugin.records.received.ask and acks only after the plugin applied it.
// Wire rules as /api/sync/no-sales: per-till bearer (the sender is the
// bearer's till, never a body field), a replaced main refuses (kept queued).
// Nothing here runs unless a plugin declared records_forward.

const (
	// pluginRecordsMaxBody caps one POST: at most RecordWireBatchMax
	// records of RecordMaxPayload each, plus envelope. Mirrors
	// auth.exemptBodyOverrides (TestHandlerCapsFitExemptBodyLimit).
	pluginRecordsMaxBody = 3 << 19 // 1.5 MiB
	// pluginRecordsRecordDeadline bounds one plugin apply.
	pluginRecordsRecordDeadline = 5 * time.Second
	// pluginRecordsPushBatches caps batches one push tick sends.
	pluginRecordsPushBatches = 5
	// pluginRecordsRespCap bounds what the replica reads of an answer.
	pluginRecordsRespCap = 1 << 20
	// pluginRecordsProblemPrefix keys the replica's Problems entry.
	pluginRecordsProblemPrefix = "plugin_records.rejected."
)

// pluginRecordsBatchDeadline bounds one batch's deliveries; the rest are
// reported retry. A var so a test can shorten it.
var pluginRecordsBatchDeadline = 15 * time.Second

func pluginRecordsProblemKey(pluginID string) string { return pluginRecordsProblemPrefix + pluginID }

// pluginRecordWire is one record on the wire. plugin_id is stamped by the
// replica's core from the stored row, which was stamped from the calling
// module; the main re-validates everything.
type pluginRecordWire struct {
	PluginID   string          `json:"plugin_id"`
	Collection string          `json:"collection"`
	ID         string          `json:"id"`
	CreatedAt  string          `json:"created_at"`
	Payload    json.RawMessage `json:"payload"`
}

type pluginRecordResult struct {
	PluginID   string `json:"plugin_id"`
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
}

// Per-record statuses (the replica's action in parentheses).
const (
	recStatusApplied   = "applied"   // delete the row
	recStatusDuplicate = "duplicate" // delete the row
	recStatusRejected  = "rejected"  // terminal: mark rejected
	recStatusRetry     = "retry"     // keep, back off
)

// registerSyncPluginRecords mounts the main-till receiver.
func registerSyncPluginRecords(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("POST /api/sync/plugin-records", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(w, r, tills)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
			return
		}
		if refuseIfReplaced(w, r, d) {
			return
		}
		// A joined till (a replica or satellite) is not where records land;
		// the sender keeps them and re-checks its topology.
		replica, err := data.NewSettingsRepo(d.Db).IsReplica(r.Context())
		if err != nil {
			common.LocalizedError(w, r, http.StatusInternalServerError, "sync.error.apply_failed")
			return
		}
		if replica {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "not_main"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, pluginRecordsMaxBody)
		var req struct {
			Records []pluginRecordWire `json:"records"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				common.LocalizedError(w, r, http.StatusRequestEntityTooLarge, "sync.error.bad_batch")
				return
			}
			common.LocalizedError(w, r, http.StatusBadRequest, "sync.error.bad_batch")
			return
		}
		if len(req.Records) > data.RecordWireBatchMax {
			common.LocalizedError(w, r, http.StatusBadRequest, "sync.error.bad_batch")
			return
		}

		bctx, cancel := context.WithTimeout(r.Context(), pluginRecordsBatchDeadline)
		defer cancel()
		recs := data.NewPluginRecordsRepo(d.Db)
		results := make([]pluginRecordResult, 0, len(req.Records))
		counts := map[string]int{}
		for _, rec := range req.Records {
			status, reason := deliverPluginRecord(bctx, d, recs, till.ID, rec)
			counts[status]++
			results = append(results, pluginRecordResult{PluginID: rec.PluginID, Collection: rec.Collection, ID: rec.ID, Status: status, Reason: reason})
		}
		if counts[recStatusApplied] > 0 {
			// Bound the dedupe ledger (90 days, 200,000 rows per plugin).
			if err := recs.InboxTrim(r.Context(), time.Now().UTC()); err != nil {
				logging.L().Warnf("plugin records: trim inbox: %v", err)
			}
		}
		if len(results) > 0 {
			_ = posRepo.InsertAudit(r.Context(), nil, "system", "till", till.ID, "plugin_records_synced",
				map[string]any{"applied": counts[recStatusApplied], "duplicate": counts[recStatusDuplicate],
					"rejected": counts[recStatusRejected], "retry": counts[recStatusRetry]},
				time.Now().UTC().Format(time.RFC3339), "")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"results": results}, "error": nil})
	})
}

// deliverPluginRecord validates one record against the main till's own
// state and, when it passes, asks the plugin to apply it. External input:
// every field is checked here whatever the replica claims.
func deliverPluginRecord(ctx context.Context, d *common.Deps, recs *data.PluginRecordsRepo, fromTill string, rec pluginRecordWire) (status, reason string) {
	reject := func(why string) (string, string) { return recStatusRejected, why }
	retry := func(why string) (string, string) { return recStatusRetry, why }

	if len(rec.Payload) > data.RecordMaxPayload {
		return reject("too_large")
	}
	if rec.PluginID == "" || len(rec.PluginID) > 128 ||
		!data.RecordCollectionRe.MatchString(rec.Collection) || !data.RecordIDRe.MatchString(rec.ID) ||
		!data.IsJSONObject(rec.Payload) {
		return reject("invalid")
	}
	if _, err := time.Parse(time.RFC3339, rec.CreatedAt); err != nil {
		return reject("invalid")
	}
	installed, active, err := recs.PluginState(ctx, rec.PluginID)
	if err != nil {
		return retry("db_error")
	}
	if !installed {
		return reject("plugin_unknown")
	}
	if !active {
		return reject("plugin_inactive")
	}
	m, found, err := plugins.InstalledManifest(ctx, d.Db, rec.PluginID)
	if err != nil {
		return retry("manifest_unreadable")
	}
	if !found || m.RecordsForward == nil || !containsString(m.RecordsForward.Collections, rec.Collection) {
		return reject("collection_refused")
	}
	for _, perm := range []string{plugins.RecordsSyncPermission, "events:receive"} {
		granted, err := plugins.CheckPermissionGranted(ctx, d.Db, rec.PluginID, perm)
		if err != nil {
			return retry("db_error")
		}
		if !granted {
			return reject("refused")
		}
	}
	hook, err := data.NewPluginRepo(d.Db).HasActiveHook(ctx, rec.PluginID, plugins.RecordsReceivedEvent)
	if err != nil {
		return retry("db_error")
	}
	if !hook {
		return reject("refused")
	}
	dup, err := recs.InboxHas(ctx, rec.PluginID, rec.Collection, rec.ID)
	if err != nil {
		return retry("db_error")
	}
	if dup {
		return recStatusDuplicate, ""
	}
	if ctx.Err() != nil {
		return retry("batch_deadline")
	}
	timeout := pluginRecordsRecordDeadline
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < timeout {
		timeout = time.Until(dl)
	}
	raw, answered, err := boundedAskPlugin(ctx, d, "pages.pluginRecordsAsk", rec.PluginID, plugins.RecordsReceivedEvent, map[string]any{
		"collection": rec.Collection, "id": rec.ID, "from_till_id": fromTill,
		"created_at": rec.CreatedAt, "payload": rec.Payload,
	}, timeout)
	if err != nil || !answered {
		return retry("plugin_unavailable")
	}
	var ans struct {
		OK     bool   `json:"ok"`
		Retry  bool   `json:"retry"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(raw, &ans) != nil {
		return retry("bad_answer")
	}
	switch {
	case ans.OK:
		// Apply, then ledger: a crash between the two re-delivers, so the
		// plugin's apply must be idempotent.
		if err := recs.InboxAdd(ctx, rec.PluginID, rec.Collection, rec.ID, fromTill, time.Now().UTC()); err != nil {
			return retry("db_error")
		}
		return recStatusApplied, ""
	case ans.Retry:
		return retry("plugin_retry")
	default:
		return reject("refused")
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// syncPushPluginRecords is the replica's plugin-records step of
// syncPushTick: sweep the outbox, then send due rows in batches of at most
// RecordWireBatchMax. Never blocks a sale: it runs in the sync loop (which
// pauses during commit bursts) and touches only the outbox and the network.
// 401/403/404/409/429/5xx/network keep every row and back off; a malformed-
// batch 4xx rejects the batch; a 200 acts per record.
func syncPushPluginRecords(ctx context.Context, d *common.Deps, client *http.Client, primary, bearer string) {
	repo := data.NewPluginRecordsRepo(d.Db)
	if res, err := repo.Sweep(ctx, time.Now().UTC()); err != nil {
		logging.L().Warnf("plugin records: sweep outbox: %v", err)
	} else if res.Expired > 0 {
		logging.L().Warnf("plugin records: %d queued record(s) older than %d days were rejected as expired", res.Expired, int(data.RecordPendingMaxAge.Hours()/24))
	}
	defer refreshPluginRecordsProblems(ctx, repo)

	for i := 0; i < pluginRecordsPushBatches; i++ {
		now := time.Now().UTC()
		due, err := repo.Due(ctx, now, data.RecordWireBatchMax, data.RecordWireBatchBytes)
		if err != nil {
			logging.L().Errorf("plugin records: read outbox: %v", err)
			return
		}
		if len(due) == 0 || !pushPluginRecordsBatch(ctx, d, repo, client, primary, bearer, due) {
			return
		}
	}
}

// pushPluginRecordsBatch sends one batch and reports whether the caller may
// send the next (only after a 200).
func pushPluginRecordsBatch(ctx context.Context, d *common.Deps, repo *data.PluginRecordsRepo, client *http.Client, primary, bearer string, due []data.OutboxRecord) bool {
	wire := make([]pluginRecordWire, 0, len(due))
	for _, o := range due {
		wire = append(wire, pluginRecordWire{PluginID: o.PluginID, Collection: o.Collection, ID: o.RecordID,
			CreatedAt: o.CreatedAt, Payload: json.RawMessage(o.Payload)})
	}
	raw, err := json.Marshal(map[string]any{"records": wire})
	if err != nil {
		return false
	}
	backoffAll := func() {
		now := time.Now().UTC()
		for _, o := range due {
			if err := repo.Retry(ctx, o.Seq, now, rand.Float64()*2-1); err != nil {
				logging.L().Warnf("plugin records: schedule retry: %v", err)
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(primary, "/")+"/api/sync/plugin-records", bytes.NewReader(raw))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Infof("plugin records push: main till unreachable (%v) — will retry", err)
		backoffAll()
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, pluginRecordsRespCap))

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		// A batch built within the bounds is never malformed: a bug path.
		logging.L().Errorf("plugin records push: main till refused the batch as malformed (%s) — rejecting %d record(s)", resp.Status, len(due))
		now := time.Now().UTC()
		for _, o := range due {
			rejectOutboxRow(ctx, d, repo, o, data.RecordReasonBadBatch, now)
		}
		return false
	default:
		// 401 (a rotated bearer heals it), 403 not_main, 404 (older main),
		// 409 replaced, 429, 5xx: link or topology conditions, never data loss.
		logging.L().Warnf("plugin records push: %s — will retry", resp.Status)
		backoffAll()
		return false
	}

	var env struct {
		Data struct {
			Results []pluginRecordResult `json:"results"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &env) != nil {
		backoffAll()
		return false
	}
	type key struct{ p, c, id string }
	byKey := make(map[key]pluginRecordResult, len(env.Data.Results))
	for _, r := range env.Data.Results {
		byKey[key{r.PluginID, r.Collection, r.ID}] = r
	}
	now := time.Now().UTC()
	delivered := 0
	for _, o := range due {
		res, ok := byKey[key{o.PluginID, o.Collection, o.RecordID}]
		switch {
		case ok && (res.Status == recStatusApplied || res.Status == recStatusDuplicate):
			if err := repo.Delete(ctx, o.Seq); err != nil {
				logging.L().Warnf("plugin records: delete delivered row: %v", err)
			}
			delivered++
		case ok && res.Status == recStatusRejected:
			rejectOutboxRow(ctx, d, repo, o, res.Reason, now)
		default: // retry, an unknown status, or no answer for the row
			if err := repo.Retry(ctx, o.Seq, now, rand.Float64()*2-1); err != nil {
				logging.L().Warnf("plugin records: schedule retry: %v", err)
			}
		}
	}
	at := recordMainContact(ctx, d, time.Now())
	_ = d.Settings.Set(ctx, "sync.last_push_at", at)
	if delivered > 0 {
		logging.L().Infof("sync push: %d plugin record(s) delivered to the main till", delivered)
	}
	return true
}

// rejectOutboxRow makes a row terminal and audits it (the card: "4xx =
// terminal rejected, audited, shown in Problems").
func rejectOutboxRow(ctx context.Context, d *common.Deps, repo *data.PluginRecordsRepo, o data.OutboxRecord, reason string, now time.Time) {
	if reason == "" {
		reason = "refused"
	}
	if err := repo.MarkRejected(ctx, o.Seq, reason, now); err != nil {
		logging.L().Warnf("plugin records: mark rejected: %v", err)
		return
	}
	_ = data.NewPOSRepo(d.Db).InsertAudit(ctx, nil, "system", "plugin", o.PluginID, "plugin_record_rejected",
		map[string]any{"collection": o.Collection, "id": o.RecordID, "reason": reason}, now.Format(time.RFC3339), "")
}

// refreshPluginRecordsProblems keeps one keyed Problem per plugin that has
// rejected rows, and resolves it when none remain.
func refreshPluginRecordsProblems(ctx context.Context, repo *data.PluginRecordsRepo) {
	rejected, err := repo.RejectedByPlugin(ctx)
	if err != nil {
		return
	}
	for plugin, n := range rejected {
		if k := pluginRecordsProblemKey(plugin); !logging.HasOpenProblem(k) {
			logging.L().WarnProblemf(k, "plugin %s: %d record(s) could not be delivered to the main till and were rejected", plugin, n)
		}
	}
	logging.ResolveProblemsWhere(func(key string) bool {
		if !strings.HasPrefix(key, pluginRecordsProblemPrefix) {
			return false
		}
		_, still := rejected[strings.TrimPrefix(key, pluginRecordsProblemPrefix)]
		return !still
	})
}
