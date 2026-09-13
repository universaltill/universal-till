package cloudsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/issuereport"
)

// ADR-0092 §6 (till side): a bundle that carries a diagnostic-session
// snapshot uploads it over the EXISTING multipart issue-report request —
// two extra fields, no new endpoint — and a bundle without one sends
// neither field (a cloud predating the snapshot pointer sees exactly what
// it always saw).
func TestUploadIssueReportSendsDiagnosticAttachment(t *testing.T) {
	var gotSession string
	var gotEvents []string
	var sawSessionField, sawEventsField bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		_, sawSessionField = r.MultipartForm.Value["diagnostic_session_id"]
		_, sawEventsField = r.MultipartForm.Value["diagnostic_events"]
		gotSession = r.FormValue("diagnostic_session_id")
		gotEvents = nil
		if raw := r.FormValue("diagnostic_events"); raw != "" {
			var evs []json.RawMessage
			if err := json.Unmarshal([]byte(raw), &evs); err != nil {
				t.Errorf("diagnostic_events is not a JSON array: %v", err)
			}
			for _, e := range evs {
				gotEvents = append(gotEvents, string(e))
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	with := issuereport.Bundle{Meta: issuereport.Meta{
		ID: "r-1", Note: "n", CreatedAt: time.Now(),
		DiagnosticSessionID: "sess-9",
		DiagnosticEvents:    []json.RawMessage{json.RawMessage(`{"type":"diagnostic_gap","dropped_count":2}`)},
	}, Dir: t.TempDir()}
	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), with); err != nil {
		t.Fatal(err)
	}
	if gotSession != "sess-9" || len(gotEvents) != 1 || gotEvents[0] != `{"type":"diagnostic_gap","dropped_count":2}` {
		t.Fatalf("session=%q events=%v", gotSession, gotEvents)
	}

	without := issuereport.Bundle{Meta: issuereport.Meta{ID: "r-2", Note: "n", CreatedAt: time.Now()}, Dir: t.TempDir()}
	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), without); err != nil {
		t.Fatal(err)
	}
	if sawSessionField || sawEventsField {
		t.Fatal("a bundle with no snapshot must not send the diagnostic fields at all")
	}
}
