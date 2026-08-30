package ui

import (
	"html/template"
	"io"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

type JournalView struct {
	Tpl *template.Template
}

type JournalViewData struct {
	Entries []data.SaleJournalEntry
	OOB     bool

	// ShowFilters/Tills/SelectedTill/Day are ut-docs#550's cross-till
	// filtering — only the full /ui/journal page sets ShowFilters: true.
	// The sale-screen OOB mini-widget push (internal/pages/pos_api.go)
	// builds its own JournalViewData{Entries, OOB} and leaves these at
	// their zero values, so its appearance stays unchanged.
	ShowFilters  bool
	Tills        []data.TillRow
	SelectedTill string
	Day          string

	// IsReplica/ExplicitCrossTillOnReplica are the B2 fix (ut-docs#550
	// review): a replica's own sales table never accumulates sibling
	// tills' sales (they push one-way to the primary), so an explicit
	// cross-till ask on a replica can never be satisfied locally.
	// ExplicitCrossTillOnReplica is only true when IsReplica is true AND
	// the operator explicitly asked for cross-till data (not the bare
	// default view, which is honest by construction on a replica) — see
	// internal/pages/journal_page.go for how it's computed.
	IsReplica                  bool
	ExplicitCrossTillOnReplica bool

	// Truncated is ut-docs#774: true when ListSalesJournal capped the
	// result at its limit and more rows exist for this filter — the
	// sale-screen OOB mini-widget (see above) never sets this either, so
	// it never shows the notice.
	Truncated bool
}

// NewJournalView's file set is fixed, so it's parsed once and cloned per
// call thereafter (ut-docs#1320) — see httpx.ClonedTemplate. Called on
// every completed sale (pos_api.go) and every /ui/journal load.
func NewJournalView(funcs template.FuncMap) (*JournalView, error) {
	t, err := httpx.ClonedTemplate("ui.JournalView", "base.html", funcs,
		"ui/partials/journal.html",
	)
	if err != nil {
		return nil, err
	}
	return &JournalView{Tpl: t}, nil
}

func (v *JournalView) Render(w io.Writer, data JournalViewData) error {
	return v.Tpl.ExecuteTemplate(w, "journal", data)
}
