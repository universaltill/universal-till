package data

import (
	"context"
	"fmt"
)

// UserViewRow is one row of the users.list.v1 core read view (ADR-0149 §6,
// ut-docs#3976). Its JSON shape is a published contract (ut-docs
// reference/contracts/plugin-views.md, pinned by
// TestCoreViewRowShapesArePinned): never add a page-only field here, and
// never a credential or authority detail — no username, PIN hash or role.
type UserViewRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Active      bool   `json:"active"`
}

// ListUserViewRows returns the staff list for users.list.v1: every operator,
// inactive ones included (Active says which), ordered by display name
// (SQLite NOCASE: ASCII case-insensitive) then id. The built-in "system"
// service identity is left out, exactly as the Users admin page hides it
// (users_page.go) — it is not a person. It selects only the three published
// columns, so nothing else can leak through a scan.
func (r *AuthRepo) ListUserViewRows(ctx context.Context) ([]UserViewRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, display_name, is_active
		FROM users
		WHERE id != 'system'
		ORDER BY display_name COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("list user view rows: %w", err)
	}
	defer rows.Close()
	out := []UserViewRow{}
	for rows.Next() {
		var u UserViewRow
		var active int
		if err := rows.Scan(&u.ID, &u.DisplayName, &active); err != nil {
			return nil, fmt.Errorf("scan user view row: %w", err)
		}
		u.Active = active != 0
		out = append(out, u)
	}
	return out, rows.Err()
}
