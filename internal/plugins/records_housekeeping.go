package plugins

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// RecordsProblemPrefix keys the Problems entry for a plugin whose queued
// records were rejected (ut-docs#3697).
const RecordsProblemPrefix = "plugin_records.rejected."

// RecordsProblemKey is the Problems key for one plugin's rejected records.
func RecordsProblemKey(pluginID string) string { return RecordsProblemPrefix + pluginID }

// RecordsHousekeeping sweeps the plugin-records outbox (pending rows past
// RecordPendingMaxAge become rejected "expired"; old and surplus rejected
// rows go) and refreshes the rejected-records Problems. It runs from the
// hourly housekeeping loop on every till, whatever its role (review M2): a
// promoted till, or one that left its sync group, has no push step but still
// holds rows. Failures are logged; nothing here blocks selling.
func RecordsHousekeeping(ctx context.Context, db *sql.DB, now time.Time) {
	if db == nil {
		return
	}
	repo := data.NewPluginRecordsRepo(db)
	SweepRecords(ctx, repo, now)
	RefreshRecordsProblems(ctx, repo)
}

// SweepRecords runs the outbox sweep once and logs what expired.
func SweepRecords(ctx context.Context, repo *data.PluginRecordsRepo, now time.Time) {
	res, err := repo.Sweep(ctx, now.UTC())
	if err != nil {
		logging.L().Warnf("plugin records: sweep outbox: %v", err)
		return
	}
	if res.Expired > 0 {
		logging.L().Warnf("plugin records: %d queued record(s) older than %d days were rejected as expired", res.Expired, int(data.RecordPendingMaxAge.Hours()/24))
	}
}

// RefreshRecordsProblems keeps one keyed Problem per plugin that has
// rejected rows, and resolves it when none remain.
func RefreshRecordsProblems(ctx context.Context, repo *data.PluginRecordsRepo) {
	rejected, err := repo.RejectedByPlugin(ctx)
	if err != nil {
		return
	}
	for plugin, n := range rejected {
		if k := RecordsProblemKey(plugin); !logging.HasOpenProblem(k) {
			logging.L().WarnProblemf(k, "plugin %s: %d record(s) could not be delivered to the main till and were rejected", plugin, n)
		}
	}
	logging.ResolveProblemsWhere(func(key string) bool {
		if !strings.HasPrefix(key, RecordsProblemPrefix) {
			return false
		}
		_, still := rejected[strings.TrimPrefix(key, RecordsProblemPrefix)]
		return !still
	})
}
