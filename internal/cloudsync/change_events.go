package cloudsync

import (
	"context"
	"database/sql"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

// Change events on the sync POST (ADR-0200, ut-docs#6559). The till's outbox
// (data.ChangeEventsRepo) holds version and plugin events; the next POST
// carries up to ChangeEventBatch of them, oldest first, as `events` on the
// reporting device. They are deleted only after a 2xx or 402 answer; any other
// outcome keeps them for the next POST. A waiting event forces a POST past a
// conditional-check-in 304 (the state hash ignores events), and nothing is
// attached while the registration guards say no.

// changeEventsField (not "...Key": internal/data scans *Key consts as settings keys) is the device-record key the events ride under.
const changeEventsField = "events"

// sentEvents is what one tick attached.
type sentEvents struct {
	repo *data.ChangeEventsRepo
	ids  []string
}

func (s sentEvents) pending() bool { return len(s.ids) > 0 }

// ack deletes the attached events. Best-effort: a failed delete only means the
// same ids go again, which the cloud dedupes by id.
func (s sentEvents) ack(ctx context.Context) {
	if len(s.ids) == 0 {
		return
	}
	if err := s.repo.Ack(ctx, s.ids); err != nil {
		logging.L().Warnf("cloudsync: change events delivered but not cleared (they will be re-sent and deduplicated): %v", err)
	}
}

// attachChangeEvents puts the waiting events on req's device record.
func attachChangeEvents(ctx context.Context, cfg *config.Config, db *sql.DB, settings *data.SettingsRepo, req *syncRequest) sentEvents {
	if len(req.devices) != 1 {
		return sentEvents{}
	}
	if ok, _ := enroll.ReportingAllowed(ctx, cfg, settings); !ok {
		return sentEvents{}
	}
	repo := data.NewChangeEventsRepo(db)
	evs, err := repo.ListPending(ctx, data.ChangeEventBatch)
	if err != nil {
		logging.L().Debugf("cloudsync: change events not read: %v", err)
		return sentEvents{}
	}
	if len(evs) == 0 {
		return sentEvents{}
	}
	wire := make([]map[string]any, 0, len(evs))
	ids := make([]string, 0, len(evs))
	for _, ev := range evs {
		m := map[string]any{
			"id":   ev.ID,
			"kind": ev.Kind,
			"at":   ev.At.UTC().Format(time.RFC3339),
		}
		if ev.PluginID != "" {
			m["plugin_id"] = ev.PluginID
		}
		if ev.Action != "" {
			m["action"] = ev.Action
		}
		if ev.From != "" {
			m["from"] = ev.From
		}
		if ev.To != "" {
			m["to"] = ev.To
		}
		wire = append(wire, m)
		ids = append(ids, ev.ID)
	}
	req.devices[0][changeEventsField] = wire
	return sentEvents{repo: repo, ids: ids}
}

// recordLastCheckin stores when the cloud last answered this till (the
// Settings card shows it). Best-effort.
func recordLastCheckin(ctx context.Context, settings *data.SettingsRepo, now time.Time) {
	if err := settings.Set(ctx, enroll.KeyLastCheckinAt, now.UTC().Format(time.RFC3339)); err != nil {
		logging.L().Debugf("cloudsync: last check-in not recorded: %v", err)
	}
}
