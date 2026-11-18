package plugins

import (
	"context"
	"database/sql"

	"github.com/universaltill/universal-till/internal/logging"
)

// DeclaredPermissionNames is the set of permission names a plugin already has
// rows for (declared by an earlier version). Read it BEFORE PersistManifest
// adds the new version's rows and hand it to GrantDeclaredPermissions.
func DeclaredPermissionNames(ctx context.Context, db *sql.DB, pluginID string) (map[string]bool, error) {
	prev, err := ListPluginPermissions(ctx, db, pluginID)
	out := make(map[string]bool, len(prev))
	for _, p := range prev {
		out[p.Name] = true
	}
	return out, err
}

// GrantDeclaredPermissions grants the manifest's declared permissions by the
// one rule every reviewed install uses (installGrantsPermission, ut-docs#4145,
// ADR-0153 Decision 5): a permission is granted on its FIRST declaration only,
// so an operator's revoke survives an update, and the ADR-0153 ungranted set
// stays ungranted. previouslyDeclared comes from DeclaredPermissionNames;
// prevErr non-nil (the read failed) fails closed: nothing is granted.
// Used by the marketplace installer and by the bundled first-party loader
// (ut-docs#6627), whose bytes are lock-verified instead of signature-verified.
func GrantDeclaredPermissions(ctx context.Context, db *sql.DB, m *Manifest, previouslyDeclared map[string]bool, prevErr error) {
	for _, perm := range m.Permissions {
		if !installGrantsPermission(m, perm, prevErr != nil || previouslyDeclared[perm]) {
			continue
		}
		if err := GrantPermission(ctx, db, m.ID, perm); err != nil {
			logging.L().Warnf("grant declared permission %s to %s: %v", perm, m.ID, err)
		}
	}
}
