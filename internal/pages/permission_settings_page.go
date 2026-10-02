package pages

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// lockoutAction/lockoutRole are the one grant this page can never let go to
// zero: the (super_admin, permission_management) cell gates this page
// itself. Revoking it would permanently lock every super_admin — including
// the one making the change — out of the only surface that can grant it
// back (ut-docs#556's own acceptance criteria).
const (
	lockoutRole   = "super_admin"
	lockoutAction = "permission_management"
)

// registerPermissionSettings wires the super_admin-only role→action
// permission-matrix editor (ut-docs#556, split (c) of #520). Dogfoods the
// #554 Can() mechanism it edits: the page is itself gated on the
// `permission_management` action, seeded super_admin-only (migration 047).
func registerPermissionSettings(mux *http.ServeMux, d *common.Deps) {
	authRepo := data.NewAuthRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	type gridCell struct {
		Granted bool
		Locked  bool // this exact cell can't be unchecked (self-lockout guard)
	}
	type actionRow struct {
		Action  string
		Cells   map[string]gridCell // by role
		Unlocks string              // translated, joined menu labels this action shows ("" = none)
	}
	type groupView struct {
		Key  string
		Rows []*actionRow
	}
	unlocksByAction := menuUnlocksByAction()

	mux.HandleFunc("GET /users/permissions", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, lockoutAction) {
			httpx.RenderError(w, r, http.StatusForbidden, "permissions.error.super_admin_required", nil)
			return
		}
		grants, err := authRepo.ListRolePermissionMatrix(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		customRoles, err := authRepo.ListCustomRoles(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		isCustom := make(map[string]bool, len(customRoles))
		for _, ri := range customRoles {
			isCustom[ri.Role] = true
		}

		var roles []string
		seenRole := map[string]bool{}
		rowByAction := map[string]*actionRow{}
		var rows []*actionRow
		for _, g := range grants {
			if _, hidden := permissionHiddenActions[g.Action]; hidden {
				// Still collect the role column, just never render the row.
				if !seenRole[g.Role] {
					seenRole[g.Role] = true
					roles = append(roles, g.Role)
				}
				continue
			}
			if !seenRole[g.Role] {
				seenRole[g.Role] = true
				roles = append(roles, g.Role)
			}
			row, ok := rowByAction[g.Action]
			if !ok {
				row = &actionRow{Action: g.Action, Cells: map[string]gridCell{}}
				rowByAction[g.Action] = row
				rows = append(rows, row)
			}
			row.Cells[g.Role] = gridCell{
				Granted: g.Granted,
				Locked:  g.Role == lockoutRole && g.Action == lockoutAction,
			}
		}

		// "Unlocks" (ut-docs#3132, display only): the menu entries whose
		// VisibleIf resolves to this action. A composite predicate (e.g. a
		// country-specific fiscal tile nested under settings) is listed
		// only where this shop has that tile at all — evaluated through the
		// same menuPredicates the menu itself uses.
		locale := httpx.RequestLocale(r)
		vis := &menuVisibility{d: d, r: r}
		sep := httpx.T(locale, "permissions.unlocks_separator")
		for _, row := range rows {
			var labels []string
			for _, u := range unlocksByAction[row.Action] {
				if u.Predicate != row.Action && !vis.visible(u.Predicate) {
					continue
				}
				labels = append(labels, httpx.T(locale, u.LabelKey))
			}
			if len(labels) > 0 {
				row.Unlocks = strings.Join(labels, sep)
			}
		}

		// Group the rows (permission_groups.go); anything the table doesn't
		// place still renders, in a trailing "other" group. Hidden actions
		// (permissionHiddenActions) never reached rows above, so they land
		// in neither.
		placed := map[string]bool{}
		var groups []groupView
		for _, g := range permissionGroups {
			gv := groupView{Key: g.Key}
			for _, a := range g.Actions {
				if row, ok := rowByAction[a]; ok {
					gv.Rows = append(gv.Rows, row)
					placed[a] = true
				}
			}
			if len(gv.Rows) > 0 {
				groups = append(groups, gv)
			}
		}
		other := groupView{Key: permissionOtherGroup}
		for _, row := range rows {
			if !placed[row.Action] {
				other.Rows = append(other.Rows, row)
			}
		}
		if len(other.Rows) > 0 {
			groups = append(groups, other)
		}

		// Columns: the built-in roles in today's (key) order, then the
		// custom roles (ADR-0128 §6) by label. A custom role's column is
		// read-only here: it is edited in my.universaltill.com, and the
		// POST below refuses it too.
		var cols []permissionColumn
		for _, role := range roles {
			if !isCustom[role] {
				cols = append(cols, permissionColumn{Role: role})
			}
		}
		// seenRole comes from the matrix (roles CROSS JOIN actions), so a
		// custom role only lacks it when the till knows no action at all.
		for _, ri := range customRoles {
			if seenRole[ri.Role] {
				label := ri.Label
				if label == "" {
					label = ri.Role
				}
				cols = append(cols, permissionColumn{Role: ri.Role, Label: label, Cloud: true})
			}
		}

		httpx.Render("ui/pages/permissions.html", map[string]any{
			"title":     httpx.T(locale, "page.title.permissions"),
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Roles":     cols,
			"Groups":    groups,
			"Cols":      len(cols) + 1,
		})(w, r)
	})

	mux.HandleFunc("POST /api/users/permissions", func(w http.ResponseWriter, r *http.Request) {
		role := r.FormValue("role")
		action := r.FormValue("action")
		grantedRaw := r.FormValue("granted")
		granted := grantedRaw == "1"
		locale := httpx.ResolveLocale(w, r)

		// ADR-0115 §1: the matrix is shop-wide configuration the main till
		// owns (role_permissions rides the admin bundle, main-till-wins), so
		// a change on a till that follows a main till would be reverted by
		// its next pull -- refuse it up front, before any elevation prompt,
		// the catalog pages' requirePrimary stance. 409 with a text/html
		// fragment: app.js's htmx:beforeSwap force-swaps a non-2xx HTML body
		// into #perm-msg (ut-docs#916), so the operator sees this message,
		// not the generic server-error banner.
		if d.SyncPrimaryURL(r.Context()) != "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `<span class="login-error">%s</span>`, httpx.T(locale, "users.error.replica_use_primary"))
			return
		}

		// Validate the request body BEFORE ever checking elevation
		// (ut-docs#557 review finding): burning a manager's PIN entry on a
		// request that was always going to 400 anyway is a needless cost.
		// Also rejects clean 400s for bad input before ever touching a
		// write — role_permissions' FK constraints would also refuse an
		// unknown role/action, but as a raw SQLite error, not a message an
		// operator (or this page's own client-side JS) can act on.
		if role == "" || action == "" {
			http.Error(w, "role and action required", http.StatusBadRequest)
			return
		}
		if origin, ok, err := authRepo.RoleOrigin(r.Context(), role); err != nil {
			logging.L().Errorf("permission matrix: role exists check: %v", err)
			http.Error(w, "failed to save", http.StatusInternalServerError)
			return
		} else if !ok {
			http.Error(w, "unknown role", http.StatusBadRequest)
			return
		} else if origin == data.RoleOriginCloud {
			// ADR-0128 §3/§6: a custom role is edited only in
			// my.universaltill.com (its grants arrive by save_role
			// directive, which would overwrite a till-side edit). Same
			// 409 + text/html fragment as the replica refusal above.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `<span class="login-error">%s</span>`, httpx.T(locale, "permissions.error.cloud_role"))
			return
		}
		if ok, err := authRepo.ActionExists(r.Context(), action); err != nil {
			logging.L().Errorf("permission matrix: action exists check: %v", err)
			http.Error(w, "failed to save", http.StatusInternalServerError)
			return
		} else if !ok {
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}

		// Self-lockout guard, also ahead of elevation: no approver PIN, of
		// any role, makes this write legal — asking for one first would
		// just burn it on a request that's refused either way. Returns 200
		// (not 409): htmx never swaps a non-2xx response by default, and
		// this codebase's only override (web/public/app.js's beforeSwap) is
		// scoped to 400s under /api/pos/ — a 409 here would render as a
		// generic "server error" banner, not the actual reason, on the one
		// message this guard exists to surface.
		if role == lockoutRole && action == lockoutAction && !granted {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<span class="login-error">%s</span>`, httpx.T(locale, "permissions.lockout_error"))
			return
		}

		// Mutating + audit-writing (ut-docs#557): a denied session gets an
		// in-place PIN re-auth instead of a flat 403. lockoutAction is this
		// page's own gate (super_admin only) — the SAME action string
		// canPerform used before, so an elevating approver must themselves
		// be super_admin, not merely re-prove "some manager PIN".
		elev := checkOrElevate(d, r, lockoutAction, r.FormValue("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/users/permissions", "#perm-msg",
				permissionChangeSummary(locale, role, action, granted), []elevationHiddenField{
					{Name: "role", Value: role},
					{Name: "action", Value: action},
					{Name: "granted", Value: grantedRaw},
				}, elev)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}

		tx, err := d.Db.BeginTx(r.Context(), nil)
		if err != nil {
			logging.L().Errorf("permission matrix: begin tx: %v", err)
			http.Error(w, "failed to save", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		if err := authRepo.SetRolePermission(r.Context(), tx, role, action, granted); err != nil {
			logging.L().Errorf("permission matrix: set role permission: %v", err)
			http.Error(w, "failed to save", http.StatusInternalServerError)
			return
		}
		verb := "role_permission_revoked"
		if granted {
			verb = "role_permission_granted"
		}
		auditErr := func() error {
			payload := map[string]any{"role": role, "action": action, "granted": granted}
			now := time.Now().UTC().Format(time.RFC3339)
			if elev.Outcome == elevated {
				return posRepo.InsertAuditElevated(r.Context(), tx, actorID, elev.ActorID, "role_permission", role+":"+action, verb, payload, now, "")
			}
			return posRepo.InsertAudit(r.Context(), tx, actorID, "role_permission", role+":"+action, verb, payload, now, "")
		}()
		if auditErr != nil {
			logging.L().Errorf("permission matrix: journal write: %v", auditErr)
			http.Error(w, "failed to save", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			logging.L().Errorf("permission matrix: commit: %v", err)
			http.Error(w, "failed to save", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "permissions.saved"))
	})
}

// permissionColumn is one role column of the matrix. A built-in role's
// header is its users.role.<key> translation; a custom role (Cloud) shows
// its own Label and read-only cells.
type permissionColumn struct {
	Role  string
	Label string
	Cloud bool
}

// permissionChangeSummary renders a human-readable, pre-translated
// description of the specific role→action grant/revoke an elevation prompt
// is asking an approver to sign off on (ut-docs#557 review finding: the
// dialog previously showed only a generic "manager approval required" while
// the actual change traveled invisibly as hidden form fields — worst on
// this page specifically, since the elevated action here is a PERMANENT
// permission grant/revoke, not a transient one-off like the other two
// checkOrElevate call sites, so an unseen approval is a standing mistake,
// not just a one-time one). role/action are rendered through the SAME
// permissions.action.%s/users.role.%s keys permissions.html's own grid
// already uses (T() falls back to the raw key if a translation is somehow
// missing, never panics), so an approver sees the exact labels they'd see
// on the matrix itself.
func permissionChangeSummary(locale, role, action string, granted bool) string {
	roleLabel := httpx.T(locale, fmt.Sprintf("users.role.%s", role))
	actionLabel := httpx.T(locale, fmt.Sprintf("permissions.action.%s", action))
	if granted {
		return fmt.Sprintf(httpx.T(locale, "elevation.summary.permission_grant"), roleLabel, actionLabel)
	}
	return fmt.Sprintf(httpx.T(locale, "elevation.summary.permission_revoke"), actionLabel, roleLabel)
}
