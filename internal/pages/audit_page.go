package pages

import (
	"net/http"
	"strconv"

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
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		entityTypes, _ := repo.DistinctAuditEntityTypes(r.Context())
		actors, _ := data.NewAuthRepo(d.Db).ListUsers(r.Context())

		httpx.Render("ui/pages/audit.html", map[string]any{
			"title":       "Audit trail",
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.Menu,
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
}
