package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// syncQuarantinePageSize bounds how many quarantined entries render at
// once. There is no pagination here (unlike audit.html): a healthy shop's
// steady state is zero, and even a shop with real problems accumulates
// these one poison sale at a time — a bare cap protects the page from an
// unbounded render without needing the audit page's fuller filter/paging
// machinery for what is meant to be a short, attention-grabbing list.
const syncQuarantinePageSize = 200

// registerSyncQuarantinePage serves the quarantined-LAN-sync-journal-entry
// admin panel (ut-docs#1133, following up on ADR-0065's own "Not decided
// here" — a poison journal entry a replica pushes that the primary can't
// apply is recorded in sync_journal_quarantine, but until this page nothing
// ever read it back: the only operator-visible signal was a Warn-level line
// in the in-memory Problems ring, gone after 50 more entries or a restart,
// while both the replica's own sync chip and the primary's own sale counts
// read fully caught-up. See data.POSRepo.ListJournalQuarantine's own
// comment for the data-layer half this page finally uses.
//
// Quarantine entries are a PRIMARY-only concept: applyJournal (sync_sales.go)
// is where InsertJournalQuarantine is ever called, and that handler only
// runs on the till receiving a replica's pushed batch — i.e. the primary.
// A replica is redirected to Settings, same as the till-name edit's own
// .IsPrimaryTill guard (settings.html) and the promote-only sections of
// tills.html: showing an always-empty page on every replica would be a
// standing "nothing to see here" a manager has to learn to ignore, and
// worse, could read as "confirmed clean" on a device that was never in a
// position to know either way.
func registerSyncQuarantinePage(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
	tillsRepo := data.NewTillsRepo(d.Db)

	mux.HandleFunc("GET /sync-quarantine", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
			return
		}
		if d.SyncPrimaryURL(r.Context()) != "" {
			// A replica: quarantine is a primary-only concept (see the
			// registerSyncQuarantinePage doc comment above) — nothing this
			// till could ever show here is meaningful.
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
			return
		}

		entries, err := posRepo.ListJournalQuarantine(r.Context(), syncQuarantinePageSize)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_quarantine", err)
			return
		}

		// Resolve till_id -> a human name the same way tills.html already
		// does (ListTills, not a per-row lookup) -- one query regardless of
		// how many quarantined entries there are. A till that has since
		// been revoked still gets its old name (ListTills only returns
		// currently-enrolled tills); falling back to the raw id is honest
		// about that rather than pretending the row doesn't exist.
		tillNames := map[string]string{}
		if tills, tillsErr := tillsRepo.ListTills(r.Context()); tillsErr == nil {
			for _, t := range tills {
				tillNames[t.ID] = t.Name
			}
		}

		type quarantineRow struct {
			TillName      string
			ReceiptNo     string
			Reason        string
			QuarantinedAt string
		}
		rows := make([]quarantineRow, 0, len(entries))
		for _, e := range entries {
			name := tillNames[e.TillID]
			if name == "" {
				name = e.TillID
			}
			rows = append(rows, quarantineRow{
				TillName:      name,
				ReceiptNo:     e.ReceiptNo,
				Reason:        e.Reason,
				QuarantinedAt: e.QuarantinedAt,
			})
		}

		httpx.Render("ui/pages/sync_quarantine.html", map[string]any{
			"title":     "Quarantined sync entries",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Entries":   rows,
		})(w, r)
	})
}
