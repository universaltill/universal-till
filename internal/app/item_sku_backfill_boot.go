package app

import (
	"context"
	"database/sql"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
)

// bootLogger is the subset of *logging.Logger backfillItemSKUsOnPrimary
// uses — a seam so its tests can record what it logs.
type bootLogger interface {
	Infof(format string, args ...any)
	Errorf(format string, args ...any)
}

// backfillItemSKUsOnPrimary gives every catalog item still without a SKU
// (created before ut-docs#3087) a generated one, once per boot — a second
// boot finds nothing to do (ut-docs#3097). Only on a primary/standalone
// till: sync.primary_url empty, the same rule as
// discovery.RoleCheckFromSettings and Deps.SyncPrimaryURL. A replica's
// catalog comes from its primary, so a local write would diverge from it.
// Best-effort: a failure is logged and never blocks startup.
func backfillItemSKUsOnPrimary(ctx context.Context, sqlDB *sql.DB, log bootLogger) {
	primaryURL, _, err := data.NewSettingsRepo(sqlDB).Get(ctx, "sync.primary_url")
	if err != nil {
		log.Errorf("backfill item skus: read sync.primary_url: %v", err)
		return
	}
	if strings.TrimSpace(primaryURL) != "" {
		return
	}
	assigned, err := data.NewCatalogRepo(sqlDB).BackfillMissingItemSKUs(ctx)
	if err != nil {
		log.Errorf("backfill item skus: %v", err)
		return
	}
	if len(assigned) > 0 {
		log.Infof("assigned a generated SKU to %d catalog item(s) that had none (ut-docs#3097)", len(assigned))
	}
}
