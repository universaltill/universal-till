package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// knownIssueReportStatuses is the set of statuses this till has translations
// for: "sent" is the local-only sentinel (uploaded, no cloud status seen
// yet); the rest are the cloud's own states (ut-docs#348 contract).
var knownIssueReportStatuses = map[string]bool{
	"sent": true, "received": true, "transcribing": true,
	"ready": true, "filed": true, "discarded": true,
}

// issueReportStatusKey maps a status string to its issuereport.status.*
// translation key. Guarded: the status comes from the cloud, and httpx's T
// falls back to returning an unknown key literally — so an unrecognized
// value (a newer cloud than this till) must map to the translated "unknown"
// string, never render as a raw dotted key.
func issueReportStatusKey(status string) string {
	if !knownIssueReportStatuses[status] {
		status = "unknown"
	}
	return "issuereport.status." + status
}

// myReportRow is one rendered row of /my-reports.
type myReportRow struct {
	ID             string
	Note           string
	CapturedAt     string
	StatusKey      string
	GithubIssueURL string
	HadAudio       bool
	HadVideo       bool
	ImageCount     int
}

// registerMyReportsPage serves the manager-gated "My reports" list
// (ut-docs#348): every bug report this till has uploaded, with the
// last-known cloud status and GitHub link. Reads ONLY the local
// issue_reports_sent table — never the network — so it works fully offline
// (statuses are refreshed in the background by cloudsync's tick; offline
// just means the last-known values keep showing). Manager-gated like the
// capture panel that links here: rows carry managers' free-text notes.
func registerMyReportsPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /my-reports", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin role required", http.StatusForbidden)
			return
		}
		reports, err := data.NewIssueReportsRepo(d.Db).ListSent(r.Context(), 100)
		if err != nil {
			http.Error(w, "failed to load reports", http.StatusInternalServerError)
			return
		}
		rows := make([]myReportRow, 0, len(reports))
		for _, rec := range reports {
			rows = append(rows, myReportRow{
				ID:             rec.ID,
				Note:           rec.Note,
				CapturedAt:     rec.CapturedAt.UTC().Format("2006-01-02 15:04"),
				StatusKey:      issueReportStatusKey(rec.Status),
				GithubIssueURL: rec.GithubIssueURL,
				HadAudio:       rec.HadAudio,
				HadVideo:       rec.HadVideo,
				ImageCount:     rec.ImageCount,
			})
		}
		httpx.Render("ui/pages/my_reports.html", map[string]any{
			"title":     "My reports",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Rows":      rows,
		})(w, r)
	})
}
