package pages

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

const auditPageSize = 50

// registerAuditPage serves the audit-trail browse/filter page. Manager/admin
// only — this reads system-wide history (every sale, shift, user, settings,
// and plugin action already written via POSRepo/PluginRepo InsertAudit), not
// something scoped to the viewer.
func registerAuditPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/audit", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "audit") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required") // page-error:allow not yet migrated, tracked in ut-docs#1458
			return
		}

		repo := data.NewPOSRepo(d.Db)
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		filters := data.AuditFilters{
			EntityType: q.Get("entity_type"),
			ActorID:    q.Get("actor_id"),
			Action:     q.Get("action"),
			Since:      q.Get("since"),
			Until:      q.Get("until"),
			Limit:      auditPageSize,
			Offset:     (page - 1) * auditPageSize,
		}

		entries, err := repo.ListAudit(r.Context(), filters)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "audit.error.server", "audit", err) // page-error:allow not yet migrated, tracked in ut-docs#1458
			return
		}
		entityTypes, _ := repo.DistinctAuditEntityTypes(r.Context())
		actors, _ := data.NewAuthRepo(d.Db).ListUsers(r.Context())

		httpx.Render("ui/pages/audit.html", map[string]any{
			"title":       "Audit trail",
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.MenuSnapshot(),
			"Entries":     entries,
			"EntityTypes": entityTypes,
			"Actors":      actors,
			"EntityType":  filters.EntityType,
			"ActorID":     filters.ActorID,
			"Action":      filters.Action,
			"Since":       filters.Since,
			"Until":       filters.Until,
			"Page":        page,
			"PrevPage":    page - 1,
			"NextPage":    page + 1,
			"HasNext":     len(entries) == auditPageSize,
			"HasPrev":     page > 1,
		})(w, r)
	})

	mux.HandleFunc("GET /api/audit/export", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "audit") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}

		repo := data.NewPOSRepo(d.Db)
		q := r.URL.Query()
		filters := data.AuditFilters{
			EntityType: q.Get("entity_type"),
			ActorID:    q.Get("actor_id"),
			Action:     q.Get("action"),
			Since:      q.Get("since"),
			Until:      q.Get("until"),
		}

		entries, truncated, err := repo.ListAuditForExport(r.Context(), filters)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "audit.error.server", "audit", err)
			return
		}

		filename := auditExportFilename(time.Now().UTC(), truncated, len(entries))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`.csv"`)
		if truncated {
			w.Header().Set("X-Export-Truncated", "true")
		}
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"When", "Actor", "Entity Type", "Entity ID", "Action", "Details"})
		for _, e := range entries {
			actor := e.ActorName
			if actor == "" {
				actor = "system"
			}
			// Actor (a manager's own free-typed display_name), Entity ID, and
			// Action (plugin code can supply an arbitrary string via
			// PluginRepo.InsertAuditRaw) are the reachable fields — CreatedAt
			// is a timestamp, EntityType is always one of this codebase's own
			// literal call-site strings, and Details/DataJSON always starts
			// with '{' (ut-docs#195), so none of those three need csvSafe.
			_ = cw.Write([]string{e.CreatedAt, csvSafe(actor), e.EntityType, csvSafe(e.EntityID), csvSafe(e.Action), e.DataJSON})
		}
		if truncated {
			_ = cw.Write([]string{"", "", "", "", "TRUNCATED", "Only the newest " + strconv.Itoa(len(entries)) + " matching entries are included — narrow the date range to see older entries."})
		}
		cw.Flush()

		_ = repo.InsertAudit(r.Context(), nil, getSessionUserID(r), "audit_log", "-", "audit_exported",
			map[string]any{
				"rows": len(entries), "truncated": truncated,
				"entity_type": filters.EntityType, "actor_id": filters.ActorID,
				"action": filters.Action, "since": filters.Since, "until": filters.Until,
			},
			time.Now().UTC().Format(time.RFC3339), "")
	})
}

// auditExportFilename builds the export's download filename. A truncated
// result gets a visible "-TRUNCATED-first-N" suffix — this is a fiscal/
// compliance export, and the X-Export-Truncated response header alone is
// invisible in a normal browser download, so truncation (which drops the
// OLDEST rows in the selected range, since the underlying query is
// newest-first) must be visible in the artifact itself.
func auditExportFilename(now time.Time, truncated bool, rowCount int) string {
	name := "audit-log-" + now.Format("2006-01-02")
	if truncated {
		name += "-TRUNCATED-first-" + strconv.Itoa(rowCount)
	}
	return name
}
