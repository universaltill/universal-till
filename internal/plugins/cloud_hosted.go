package plugins

import (
	"context"
	"database/sql"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// withoutCloudHosted removes the plugins whose installed manifest does not
// run on a till (hosts without "till", ADR-0169 section 3). A manifest that
// cannot be read keeps its row: the normal load path then fails it loudly
// (mark broken) exactly as before, rather than this filter hiding it.
func withoutCloudHosted(ctx context.Context, db *sql.DB, rows []data.InstalledPluginRow) []data.InstalledPluginRow {
	out := rows[:0:0]
	for _, row := range rows {
		if row.Runtime == "wasm" {
			if m, ok, err := InstalledManifest(ctx, db, row.ID); err == nil && ok && !m.RunsOnTill() {
				logging.L().Infof("plugin %s runs in the cloud (hosts %v); a till lists it but does not run it (ADR-0169)", row.ID, m.Hosts)
				continue
			}
		}
		out = append(out, row)
	}
	return out
}
