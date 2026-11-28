package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// HandoverEvent is the core event that asks a plugin to move its own data out
// of core tables into its plugin.db (ut-docs#3157, ADR-0121 amendment
// 2026-11-28). It sits under the core root `plugin`: a plugin id may never
// start with a core root (validatePluginID) and a plugin may never raise a
// core-root event (event_publish, schedules), so only this runner raises it.
// Contract: ut-docs/reference/contracts/plugin-data-handover.md.
const HandoverEvent = "plugin.data.handover.ask"

const (
	// handoverMaxAttempts is per installed plugin version: an upgrade (the
	// fix for a failing handler) is asked again from zero.
	handoverMaxAttempts = 5
	handoverTick        = 60 * time.Second
	handoverCallTimeout = 60 * time.Second
	// handoverProgressWait is the pause after a "not done, progress" answer.
	handoverProgressWait = time.Minute
)

// handoverCaller is the part of *WasmRuntime the runner needs.
type handoverCaller interface {
	HandleEvent(ctx context.Context, pluginID string, ev Event) (json.RawMessage, error)
}

// StartHandoverRunner starts the background runner (call once; it stops when
// ctx ends). It ticks every 60 s (+ jitter), asks each due plugin once per
// tick, and never blocks a sale: the call goes through the ordinary event
// call-slot limits.
func StartHandoverRunner(ctx context.Context, db *sql.DB, rt *WasmRuntime) {
	if db == nil || rt == nil {
		return
	}
	go func() {
		defer logging.RecoverAndLog("plugins.handoverRunner")
		t := time.NewTimer(handoverTick / 4)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			runHandoverPass(ctx, db, rt, time.Now())
			t.Reset(handoverTick + time.Duration(time.Now().UnixNano()%int64(5*time.Second)))
		}
	}()
}

var handoverMu sync.Mutex // one pass at a time

// runHandoverPass asks every due plugin once and returns how many were asked.
// The payload is built here, by core only; the guest's answer is read as
// {"done":bool,"progress":bool} and nothing else. A plugin that is
// still copying answers {"done":false,"progress":true}: that is not a failed
// attempt, so a big table is never abandoned after five calls.
func runHandoverPass(ctx context.Context, db *sql.DB, c handoverCaller, now time.Time) int {
	handoverMu.Lock()
	defer handoverMu.Unlock()
	repo := data.NewPluginHandoverRepo(db)
	due, err := repo.ListDue(ctx, HandoverEvent, now, handoverMaxAttempts)
	if err != nil {
		logging.L().Warnf("handover: list: %v", err)
		return 0
	}
	asked := 0
	for _, cand := range due {
		if ctx.Err() != nil {
			return asked
		}
		asked++
		payload, _ := json.Marshal(map[string]string{"plugin": cand.PluginID, "to_version": cand.Version})
		cctx, cancel := context.WithTimeout(ctx, handoverCallTimeout)
		out, err := c.HandleEvent(cctx, cand.PluginID, Event{
			ID:        "handover-" + cand.PluginID,
			Type:      HandoverEvent,
			Timestamp: now,
			Payload:   payload,
		})
		cancel()
		if err != nil && strings.Contains(err.Error(), "module not loaded") {
			continue // runtime not ready yet: not the plugin's failure, no attempt counted
		}
		done := false
		progress := false
		reason := ""
		if err != nil {
			reason = err.Error()
		} else {
			var ans struct {
				Done     bool `json:"done"`
				Progress bool `json:"progress"`
			}
			if jerr := json.Unmarshal(out, &ans); jerr != nil {
				reason = "answer is not {\"done\":bool}"
			} else if ans.Done {
				done = true
			} else if ans.Progress {
				progress = true
			} else {
				reason = "not done"
			}
		}
		if done {
			if err := repo.MarkDone(ctx, cand.PluginID, now); err != nil {
				logging.L().Warnf("handover: mark done %s: %v", cand.PluginID, err)
			}
			continue
		}
		if progress {
			if err := repo.RecordProgress(ctx, cand.PluginID, cand.Version, now, now.Add(handoverProgressWait)); err != nil {
				logging.L().Warnf("handover: progress %s: %v", cand.PluginID, err)
			}
			continue
		}
		if len(reason) > 200 {
			reason = reason[:200]
		}
		backoff := time.Duration(1<<uint(cand.Attempts+1)) * time.Minute // 2,4,8,16,32 min
		if err := repo.RecordAttempt(ctx, cand.PluginID, cand.Version, now, now.Add(backoff), reason); err != nil {
			logging.L().Warnf("handover: record %s: %v", cand.PluginID, err)
		}
	}
	return asked
}
