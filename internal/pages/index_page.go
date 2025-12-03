package pages

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerIndex(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		methods := collectPaymentMethods(r.Context(), d.Db)
		defaultMethod := "cash"
		if len(methods) > 0 {
			defaultMethod = methods[0]
		}
		data := map[string]any{
			"title":                "Universal Till",
			"theme":                d.State.Theme,
			"menuItems":            d.Menu,
			"currency":             d.State.Currency,
			"paymentMethods":       methods,
			"paymentMethodDefault": defaultMethod,
		}
		httpx.Render("ui/pages/index.html", data)(w, r)
	})
}

func collectPaymentMethods(ctx context.Context, db *sql.DB) []string {
	defaults := []string{"cash", "card", "voucher", "account"}
	if db == nil {
		return defaults
	}
	rows, err := db.QueryContext(ctx, `SELECT id FROM payment_methods WHERE is_active = 1 ORDER BY id`)
	if err != nil {
		return defaults
	}
	defer rows.Close()
	seen := make(map[string]struct{})
	var methods []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		methods = append(methods, id)
	}
	if len(methods) == 0 {
		return defaults
	}
	return methods
}
