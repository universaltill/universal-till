package pos

import (
	"context"
	"database/sql"
)

// UpdateSaleStatus is the tests' shorthand for UpdateSaleStatusFrom with no
// expected-status check (production callers pass the status their gate saw,
// ut-docs#7140).
func UpdateSaleStatus(ctx context.Context, sqlDB *sql.DB, saleID, status, actorID, blockedActorID, reason string, additionalTill bool) error {
	return UpdateSaleStatusFrom(ctx, sqlDB, saleID, "", status, actorID, blockedActorID, reason, additionalTill)
}
