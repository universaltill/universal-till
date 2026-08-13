package pages

import (
	"net/http"
	"sort"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/logging"
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
	capturedAtTime time.Time // sort key only; unexported so templates never see it
	StatusKey      string
	GithubIssueURL string
	HadAudio       bool
	HadVideo       bool
	ImageCount     int
	// Failing and FailReasonKey are set only for a still-pending bundle
	// (ut-docs#637) that has crossed issuereport.UploadFailingThreshold, or
	// failed for a reason that can't self-resolve by waiting (an
	// unregistered till). GithubIssueURL is always empty for these rows —
	// a bundle that never reached the cloud has no cloud-side status to
	// carry one.
	Failing       bool
	FailReasonKey string
}

// registerMyReportsPage serves the manager-gated "My reports" list
// (ut-docs#348): every bug report this till has captured — uploaded (from
// the local issue_reports_sent table, with the last-known cloud status and
// GitHub link) or still pending/failing (ut-docs#637, from the local
// issuereport.Pending() bundle directory). Reads ONLY local state — never
// the network — so it works fully offline (statuses are refreshed in the
// background by cloudsync's tick; offline just means the last-known values
// keep showing). Manager-gated like the capture panel that links here: rows
// carry managers' free-text notes.
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
				capturedAtTime: rec.CapturedAt,
				StatusKey:      issueReportStatusKey(rec.Status),
				GithubIssueURL: rec.GithubIssueURL,
				HadAudio:       rec.HadAudio,
				HadVideo:       rec.HadVideo,
				ImageCount:     rec.ImageCount,
			})
		}

		// ut-docs#637: a bundle that has never successfully uploaded never
		// reaches issue_reports_sent, so without this it's invisible here —
		// exactly the gap that ticket describes ("sees it sit at its local
		// status... with no way to tell 'on its way' from 'this will never
		// arrive'"). Merge in still-pending bundles from local disk (this
		// read never touches the network — same offline-first contract as
		// the rest of this page), at their own computed status. A listing
		// failure is only logged: one broken read must not 500 the whole
		// page when the (usually more numerous) already-sent rows loaded
		// fine.
		pending, perr := issuereport.Pending()
		if perr != nil {
			logging.L().Warnf("my-reports: listing pending issue reports: %v", perr)
		}
		for _, b := range pending {
			row := myReportRow{
				ID:             b.Meta.ID,
				Note:           b.Meta.Note,
				CapturedAt:     b.Meta.CreatedAt.UTC().Format("2006-01-02 15:04"),
				capturedAtTime: b.Meta.CreatedAt,
				HadAudio:       b.AudioPath != "",
				HadVideo:       b.VideoPath != "",
				ImageCount:     len(b.ImagePaths),
			}
			switch {
			case b.Meta.UploadFailReason == issuereport.UploadFailReasonNotRegistered:
				// Can't self-resolve by waiting — needs the shop owner to
				// finish enrolment — so flag it from the very first failed
				// attempt rather than waiting out the threshold below.
				row.Failing = true
				row.StatusKey = "issuereport.status.failing"
				row.FailReasonKey = "issuereport.status.failing_reason.not_registered"
			case b.Meta.UploadFailCount >= issuereport.UploadFailingThreshold:
				row.Failing = true
				row.StatusKey = "issuereport.status.failing"
				row.FailReasonKey = "issuereport.status.failing_reason.other"
			default:
				row.StatusKey = "issuereport.status.pending"
			}
			rows = append(rows, row)
		}
		// Newest-captured first, matching ListSent's own ordering — sent and
		// still-pending rows are otherwise built in two separate passes above.
		sort.Slice(rows, func(i, j int) bool { return rows[i].capturedAtTime.After(rows[j].capturedAtTime) })

		httpx.Render("ui/pages/my_reports.html", map[string]any{
			"title":     "My reports",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Rows":      rows,
		})(w, r)
	})
}
