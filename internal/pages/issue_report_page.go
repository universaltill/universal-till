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

// isAvailableLocale reports whether locale is one of the shipped translation
// locales (httpx.AvailableLocales) — the same set the UI's own language
// switcher offers. Empty is never "available" here (it already means
// "unknown/auto-detect" to every downstream consumer of this value), and an
// unwired translator (e.g. a test that never called httpx.InitI18n) makes
// everything unavailable rather than trusting an unchecked value through.
func isAvailableLocale(locale string) bool {
	if locale == "" {
		return false
	}
	for _, l := range httpx.AvailableLocales() {
		if l == locale {
			return true
		}
	}
	return false
}

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
		if !canPerform(d, r, "issue_reporting") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required") // page-error:allow not yet migrated, tracked in ut-docs#1458
			return
		}
		httpx.Render("ui/pages/report_issue.html", map[string]any{
			"title":     "Report an issue",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			// The capture UI lives in the shared bug-report panel now
			// (ut-docs#346); this route stays as the /menu tile's target and
			// simply lands with the panel already expanded — stamped
			// server-side so it doesn't depend on client JS having run.
			"openBugReportPanel": true,
		})(w, r)
	})

	// 🐞 chip in the nav (ut-docs#346). nav.html has no per-request data of
	// its own, so — same pattern as /ui/session-chip and /ui/sync-chip — the
	// nav carries an htmx placeholder that loads this after render. Empty
	// 200 when the operator isn't allowed to report issues, so nothing
	// appears in the nav at all.
	mux.HandleFunc("GET /ui/bugreport-chip", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "issue_reporting") {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderPartial("ui/partials/bugreport_chip.html", nil)(w, r)
	})

	mux.HandleFunc("POST /api/issue-reports", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "issue_reporting") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
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
			common.LocalizedError(w, r, http.StatusBadRequest, "common.error.invalid_upload")
			return
		}
		note := strings.TrimSpace(r.Form.Get("note"))

		var audio []byte
		if audioFile, _, err := r.FormFile("audio"); err == nil {
			defer audioFile.Close()
			audio, err = readCappedOrReject(audioFile, issueReportMaxBytes)
			if err != nil {
				http.Error(w, "voice recording too large", http.StatusBadRequest)
				return
			}
		}

		if note == "" && len(audio) == 0 {
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

		// Screenshots (ut-docs#347) are a repeatable field — unlike the
		// single-file audio/video fields above, so they come from
		// r.MultipartForm.File directly rather than r.FormFile, which only
		// ever returns the first match. Same cap-or-reject treatment as
		// audio/video: a truncated/oversized screenshot must be a 400, not a
		// silently corrupt saved file, and any failure here must not leave a
		// partial bundle behind — so nothing is saved yet at this point.
		var images [][]byte
		if r.MultipartForm != nil {
			for _, fh := range r.MultipartForm.File["image"] {
				f, err := fh.Open()
				if err != nil {
					http.Error(w, "screenshot unreadable", http.StatusBadRequest)
					return
				}
				img, err := readCappedOrReject(f, issueReportMaxBytes)
				f.Close()
				if err != nil {
					http.Error(w, "screenshot too large", http.StatusBadRequest)
					return
				}
				images = append(images, img)
			}
		}

		// Capture the operator's UI locale (ut-docs#397) exactly the way
		// every page render resolves it, so the cloud side can hand it to
		// Whisper instead of defaulting the transcript to English. Clamped
		// to the shipped locale set: ResolveLocale trusts a raw `?lang=` or
		// cookie value for template rendering (an unrecognized value there
		// just misses translations, harmless), but this value goes on to a
		// downstream service call — a hand-edited or stale value shouldn't
		// reach Whisper's language param unchecked, so fall back to ""
		// (auto-detect) for anything not in the switcher's own list.
		locale := httpx.ResolveLocale(w, r)
		if !isAvailableLocale(locale) {
			locale = ""
		}

		id, err := issuereport.Save(note, locale, audio, video, images)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "issuereport.save_error", "issuereport", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]string{"id": id}, "error": nil,
		})
	})
}
