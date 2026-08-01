package pages

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// issueReportMaxBytes caps the combined multipart upload (audio + optional
// screen recording + note). A 60s screen recording at modest bitrate plus a
// short voice note comfortably fits well under this; generous enough that a
// manager isn't fighting the tool, small enough to bound one till's local
// disk use per report.
const issueReportMaxBytes = 32 << 20

// readCappedOrReject reads at most limit bytes and errors if the source had
// more: io.LimitReader alone would silently truncate an oversized recording
// into a corrupted, unplayable file while the till still reports "Saved" —
// reading one byte past the cap turns that silent corruption into a clear
// 400 instead.
func readCappedOrReject(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("exceeds %d byte limit", limit)
	}
	return data, nil
}

// registerIssueReportPage serves the manager-gated "report an issue" panel
// (ADR-0022, spec 012): capture a typed and/or spoken description + optional
// screen recording, save locally regardless of connectivity, queue for cloud
// upload. Not reachable from the self-order kiosk surface — staff-operated
// tills and back-office only.
func registerIssueReportPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/report-issue", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin role required", http.StatusForbidden)
			return
		}
		httpx.Render("ui/pages/report_issue.html", map[string]any{
			"title":     "Report an issue",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.Menu,
		})(w, r)
	})

	mux.HandleFunc("POST /api/issue-reports", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin role required", http.StatusForbidden)
			return
		}
		// http.MaxBytesReader must run before ParseMultipartForm: once a
		// file part exceeds ParseMultipartForm's own in-memory budget it
		// spills the entire remainder to a temp file with no size check of
		// its own — without this, a request larger than intended gets
		// written to the till's disk before the readCappedOrReject checks
		// below ever run.
		r.Body = http.MaxBytesReader(w, r.Body, issueReportMaxBytes)
		if err := r.ParseMultipartForm(issueReportMaxBytes); err != nil {
			http.Error(w, "invalid upload", http.StatusBadRequest)
			return
		}
		note := r.Form.Get("note")

		var audio []byte
		if audioFile, _, err := r.FormFile("audio"); err == nil {
			defer audioFile.Close()
			audio, err = readCappedOrReject(audioFile, issueReportMaxBytes)
			if err != nil {
				http.Error(w, "voice recording too large", http.StatusBadRequest)
				return
			}
		}

		if strings.TrimSpace(note) == "" && len(audio) == 0 {
			http.Error(w, "a description (typed or spoken) is required", http.StatusBadRequest)
			return
		}

		var video []byte
		if videoFile, _, err := r.FormFile("video"); err == nil {
			defer videoFile.Close()
			video, err = readCappedOrReject(videoFile, issueReportMaxBytes)
			if err != nil {
				http.Error(w, "screen recording too large", http.StatusBadRequest)
				return
			}
		}

		id, err := issuereport.Save(note, audio, video)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]string{"id": id}, "error": nil,
		})
	})
}
