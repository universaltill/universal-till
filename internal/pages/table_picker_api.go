package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// tablePickerOption is one choice in the basket's table-assignment picker.
type tablePickerOption struct {
	ID    string
	Label string
}

// registerTablePicker serves the basket's table-assignment picker
// (ut-docs#820, ADR-0054): every enabled, currently FREE table, plus
// whichever table the current basket is already assigned to. Since
// ut-docs#1390 that second half is load-bearing rather than merely
// defensive: the live basket's own pick now writes a table_claims row, so
// ListTablesWithState DOES report the basket's own table occupied, and
// without the `s.ID != current` exception below the picker would stop
// offering the cashier the table they are already on. Deliberately
// separate from registerTables (the
// manager-gated floor-plan editor): assigning a table to an order is a
// normal checkout action any cashier can do, not floor-plan management.
// Mirrors registerSuggestions' "load an HTMX fragment under the basket"
// shape exactly.
func registerTablePicker(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("GET /ui/pos/table-picker", func(w http.ResponseWriter, r *http.Request) {
		current := d.Engine.TableID()
		// configured tracks whether this shop does table service at all --
		// ADR-0054 soft-gates the whole feature on configuration, so with no
		// enabled tables the fragment renders nothing rather than an
		// always-on "Table: none" row. Counted separately from options
		// below, which excludes occupied tables: "all tables are busy" is a
		// real state worth a message, "this shop has no tables" is not.
		//
		// ut-docs#1355: assigning a physical table to a takeaway/to-go
		// order doesn't make sense, so the whole picker (button + dialog)
		// is soft-gated off for Takeaway too, same shape as "no tables
		// configured" -- deliberately skipping the ListTablesWithState
		// query entirely rather than fetching state nobody will render.
		configured := false
		var options []tablePickerOption
		// ADR-0073 Decision 5: eligibility follows the LINES (any dine-in
		// line, or an empty basket whose default isn't takeaway), not the
		// header — a mixed basket may take a table.
		if d.Engine.HasDineInLine() {
			states, err := repo.ListTablesWithState(r.Context())
			if err != nil {
				states = nil
			}
			for _, s := range states {
				if !s.Enabled {
					continue
				}
				configured = true
				if s.Occupied && s.ID != current {
					continue
				}
				options = append(options, tablePickerOption{ID: s.ID, Label: s.Label})
			}
		}
		httpx.RenderPartial("ui/partials/table_picker.html", map[string]any{
			"Options":      options,
			"Current":      current,
			"CurrentLabel": d.Engine.TableLabel(),
			"Configured":   configured,
		})(w, r)
	})
}
