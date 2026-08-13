package cloudsync

import (
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/logging"
)

func withTempPendingDir(t *testing.T) {
	t.Helper()
	orig := issuereport.PendingDir
	issuereport.PendingDir = t.TempDir()
	t.Cleanup(func() { issuereport.PendingDir = orig })
}

func registeredCfg(url string) *config.Config {
	cfg := &config.Config{}
	cfg.Marketplace.EndpointURL = url
	cfg.Marketplace.StoreID = "store-1"
	cfg.Marketplace.MerchantToken = "tok-1"
	return cfg
}

// uploadIssueReport must build a real multipart request carrying every
// field the cloud-side receiving endpoint (ADR-0022 spec 012 Phase 2)
// expects, including an optional video and the recent-logs lines — this
// function had zero direct coverage before this batch.
//
// The bundle is built directly (not via issuereport.Save, which pulls
// Meta.Logs from the process-wide logging.Recent() ring buffer — shared,
// mutable, order-dependent across the whole test binary) so Logs and
// CreatedAt are known values this test can actually assert against,
// instead of merely asserting a field is present.
func TestUploadIssueReportSendsMultipartBundle(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "audio.webm")
	if err := os.WriteFile(audioPath, []byte("fake-audio"), 0o644); err != nil {
		t.Fatalf("write audio fixture: %v", err)
	}
	videoPath := filepath.Join(dir, "video.webm")
	if err := os.WriteFile(videoPath, []byte("fake-video"), 0o644); err != nil {
		t.Fatalf("write video fixture: %v", err)
	}
	createdAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	b := issuereport.Bundle{
		Meta: issuereport.Meta{
			ID:        "report-123",
			Note:      "printer jammed",
			Locale:    "fa",
			CreatedAt: createdAt,
			Logs: []logging.Problem{
				{At: createdAt, Level: "WARN", Msg: "printer offline"},
				{At: createdAt, Level: "ERROR", Msg: "paper jam sensor tripped"},
			},
		},
		Dir:       dir,
		AudioPath: audioPath,
		VideoPath: videoPath,
	}

	var gotStoreID, gotReportID, gotNote, gotAuth, gotCreatedAt, gotLocale string
	var gotAudio, gotVideo []byte
	var gotLogs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("server: parse multipart: %v", err)
			return
		}
		gotStoreID = r.FormValue("store_id")
		gotReportID = r.FormValue("report_id")
		gotNote = r.FormValue("note")
		gotLocale = r.FormValue("locale")
		gotCreatedAt = r.FormValue("created_at")
		gotLogs = append([]string(nil), r.MultipartForm.Value["logs"]...)
		if fh := r.MultipartForm.File["audio"]; len(fh) == 1 {
			f, _ := fh[0].Open()
			gotAudio = make([]byte, fh[0].Size)
			_, _ = f.Read(gotAudio)
			f.Close()
		}
		if fh := r.MultipartForm.File["video"]; len(fh) == 1 {
			f, _ := fh[0].Open()
			gotVideo = make([]byte, fh[0].Size)
			_, _ = f.Read(gotVideo)
			f.Close()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), b); err != nil {
		t.Fatalf("uploadIssueReport: %v", err)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotStoreID != "store-1" || gotReportID != "report-123" || gotNote != "printer jammed" {
		t.Fatalf("fields: store=%q report=%q note=%q", gotStoreID, gotReportID, gotNote)
	}
	if gotLocale != "fa" {
		t.Fatalf("locale = %q, want %q (ut-docs#397 — Whisper needs the capture-time UI locale)", gotLocale, "fa")
	}
	if gotCreatedAt != createdAt.Format("2006-01-02T15:04:05.000000000Z07:00") {
		t.Fatalf("created_at = %q", gotCreatedAt)
	}
	if string(gotAudio) != "fake-audio" {
		t.Fatalf("audio = %q", gotAudio)
	}
	if string(gotVideo) != "fake-video" {
		t.Fatalf("video = %q", gotVideo)
	}
	wantLogs := []string{
		createdAt.Format("2006-01-02T15:04:05Z07:00") + "\tWARN\tprinter offline",
		createdAt.Format("2006-01-02T15:04:05Z07:00") + "\tERROR\tpaper jam sensor tripped",
	}
	if len(gotLogs) != len(wantLogs) || gotLogs[0] != wantLogs[0] || gotLogs[1] != wantLogs[1] {
		t.Fatalf("logs = %v, want %v", gotLogs, wantLogs)
	}
}

// A bundle with screenshots must send each as its own "image" multipart
// part (repeated field name), in ImagePaths order — the cloud side reads
// them back as a slice.
func TestUploadIssueReportSendsMultipleImageParts(t *testing.T) {
	dir := t.TempDir()
	var imagePaths []string
	imageContents := []string{"fake-png-0", "fake-png-1", "fake-png-2"}
	for i, content := range imageContents {
		p := filepath.Join(dir, fmt.Sprintf("image-%d.png", i))
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write image fixture: %v", err)
		}
		imagePaths = append(imagePaths, p)
	}
	b := issuereport.Bundle{
		Meta: issuereport.Meta{
			ID:        "report-with-images",
			Note:      "screenshots attached",
			CreatedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		},
		Dir:        dir,
		ImagePaths: imagePaths,
	}

	var gotImages [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("server: parse multipart: %v", err)
			return
		}
		for _, fh := range r.MultipartForm.File["image"] {
			f, err := fh.Open()
			if err != nil {
				t.Errorf("server: open image part: %v", err)
				continue
			}
			data := make([]byte, fh.Size)
			_, _ = f.Read(data)
			f.Close()
			gotImages = append(gotImages, data)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), b); err != nil {
		t.Fatalf("uploadIssueReport: %v", err)
	}
	if len(gotImages) != len(imageContents) {
		t.Fatalf("got %d image parts, want %d", len(gotImages), len(imageContents))
	}
	for i, want := range imageContents {
		if string(gotImages[i]) != want {
			t.Errorf("image part %d = %q, want %q", i, gotImages[i], want)
		}
	}
}

// A bundle that never captured a locale (older bundle on disk from before
// ut-docs#397, or the JS-less path) must still SEND the locale field — as an
// empty string, which the cloud side reads as "no locale known, auto-detect".
// Skipping the field entirely is not the same contract, so the assertion is
// on the part's presence, not just FormValue (which can't tell empty from
// absent).
func TestUploadIssueReportSendsEmptyLocaleWhenUnknown(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("no locale captured", "", []byte("fake-audio"), nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, err := issuereport.Pending()
	if err != nil || len(bundles) != 1 {
		t.Fatalf("Pending: %v (%d)", err, len(bundles))
	}
	if bundles[0].Meta.Locale != "" {
		t.Fatalf("Meta.Locale = %q, want empty", bundles[0].Meta.Locale)
	}

	var gotLocaleValues []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		gotLocaleValues = append([]string(nil), r.MultipartForm.Value["locale"]...)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), bundles[0]); err != nil {
		t.Fatalf("uploadIssueReport: %v", err)
	}
	if len(gotLocaleValues) != 1 || gotLocaleValues[0] != "" {
		t.Fatalf("locale field values = %q, want exactly one empty string", gotLocaleValues)
	}
}

// A bundle with no video must omit the video part entirely rather than send
// an empty one — attachFile's "only if VideoPath != ”" guard.
func TestUploadIssueReportOmitsVideoWhenAbsent(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("no video", "", []byte("fake-audio"), nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, err := issuereport.Pending()
	if err != nil || len(bundles) != 1 {
		t.Fatalf("Pending: %v (%d)", err, len(bundles))
	}
	if bundles[0].VideoPath != "" {
		t.Fatalf("VideoPath = %q, want empty", bundles[0].VideoPath)
	}

	var hadVideoPart bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		hadVideoPart = len(r.MultipartForm.File["video"]) > 0
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), bundles[0]); err != nil {
		t.Fatalf("uploadIssueReport: %v", err)
	}
	if hadVideoPart {
		t.Fatal("video part sent for a bundle with no recording")
	}
}

// A note-only bundle (no voice recording) must omit the audio part entirely
// rather than send an empty one — mirrors the existing video-absent case.
func TestUploadIssueReportOmitsAudioWhenAbsent(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("typed only, no voice note", "", nil, nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, err := issuereport.Pending()
	if err != nil || len(bundles) != 1 {
		t.Fatalf("Pending: %v (%d)", err, len(bundles))
	}
	if bundles[0].AudioPath != "" {
		t.Fatalf("AudioPath = %q, want empty", bundles[0].AudioPath)
	}

	var hadAudioPart bool
	var gotNote string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		hadAudioPart = len(r.MultipartForm.File["audio"]) > 0
		gotNote = r.FormValue("note")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), bundles[0]); err != nil {
		t.Fatalf("uploadIssueReport: %v", err)
	}
	if hadAudioPart {
		t.Fatal("audio part sent for a bundle with no voice recording")
	}
	if gotNote != "typed only, no voice note" {
		t.Fatalf("note = %q", gotNote)
	}
}

func TestUploadIssueReportUnregistered(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("note", "", []byte("a"), nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, _ := issuereport.Pending()
	err := uploadIssueReport(context.Background(), &config.Config{}, bundles[0])
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("err = %v, want not registered", err)
	}
}

func TestUploadIssueReportNon200Fails(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("note", "", []byte("a"), nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, _ := issuereport.Pending()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), bundles[0]); err == nil {
		t.Fatal("want error on 500 response")
	}
}

// attachFile's own error branch: a bundle whose audio file vanished from
// disk between Pending() listing it and upload actually being attempted.
func TestUploadIssueReportMissingAudioFileErrors(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("note", "", []byte("a"), nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, _ := issuereport.Pending()
	b := bundles[0]
	// Simulate the audio file disappearing after Pending() already listed it.
	if err := os.Remove(b.AudioPath); err != nil {
		t.Fatalf("remove audio: %v", err)
	}
	err := uploadIssueReport(context.Background(), registeredCfg("http://unused.example"), b)
	if err == nil {
		t.Fatal("want error when the audio file is missing")
	}
}

// uploadPendingIssueReports drives the real Pending -> upload -> retain ->
// Discard cycle: a bundle that uploads successfully must leave a retained
// record in issue_reports_sent (status "sent") with its media directory
// discarded, and a bundle the cloud rejects must stay pending for the next
// tick to retry — with no retained record yet.
func TestUploadPendingIssueReportsFullCycle(t *testing.T) {
	withTempPendingDir(t)
	d := openMigratedDB(t, "issue_reports_cycle.db")
	okID, err := issuereport.Save("this one uploads", "", []byte("audio-ok"), []byte("video-ok"), [][]byte{[]byte("png-0"), []byte("png-1")})
	if err != nil {
		t.Fatalf("Save ok bundle: %v", err)
	}
	failID, err := issuereport.Save("this one fails", "", []byte("audio-fail"), nil, nil)
	if err != nil {
		t.Fatalf("Save failing bundle: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		if r.FormValue("report_id") == failID {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	uploadPendingIssueReports(context.Background(), registeredCfg(srv.URL), d.DB)

	remaining, err := issuereport.Pending()
	if err != nil {
		t.Fatalf("Pending after upload: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Meta.ID != failID {
		ids := make([]string, len(remaining))
		for i, b := range remaining {
			ids[i] = b.Meta.ID
		}
		t.Fatalf("remaining pending = %v, want only %q", ids, failID)
	}
	if _, err := os.Stat(filepath.Join(issuereport.PendingDir, okID)); !os.IsNotExist(err) {
		t.Fatalf("discarded bundle dir for %q should be gone, stat err = %v", okID, err)
	}

	// The core ut-docs#348 behavior change: the media is gone (above), but a
	// local record of the sent report survives.
	sent, err := data.NewIssueReportsRepo(d.DB).ListSent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListSent: %v", err)
	}
	if len(sent) != 1 || sent[0].ID != okID {
		t.Fatalf("retained records = %+v, want exactly one for %q (the failed upload must NOT be recorded as sent)", sent, okID)
	}
	rec := sent[0]
	if rec.Status != "sent" {
		t.Fatalf("retained status = %q, want %q", rec.Status, "sent")
	}
	if rec.Note != "this one uploads" || !rec.HadAudio || !rec.HadVideo || rec.ImageCount != 2 {
		t.Fatalf("retained record fields wrong: %+v", rec)
	}
}

// If the retained record cannot be persisted, the bundle must NOT be
// discarded — better to retry the whole upload next tick than to lose the
// report with no trace anywhere. Simulated with a database that has no
// issue_reports_sent table (the same shape as any other SaveSent failure).
func TestUploadPendingIssueReportsKeepsBundleWhenSaveSentFails(t *testing.T) {
	withTempPendingDir(t)
	brokenDB := testDB(t) // settings table only — SaveSent must fail
	id, err := issuereport.Save("record must not be lost", "", []byte("audio"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	uploadPendingIssueReports(context.Background(), registeredCfg(srv.URL), brokenDB)

	if _, err := os.Stat(filepath.Join(issuereport.PendingDir, id)); err != nil {
		t.Fatalf("bundle dir must survive a failed SaveSent (stat err = %v)", err)
	}
	remaining, err := issuereport.Pending()
	if err != nil || len(remaining) != 1 || remaining[0].Meta.ID != id {
		t.Fatalf("bundle must stay pending after a failed SaveSent: %v (%d)", err, len(remaining))
	}
}

// The core ut-docs#446 fix: a bundle whose cloud upload keeps succeeding but
// whose local retained-record save keeps failing (e.g. disk full, DB briefly
// read-only) must NOT be re-uploaded forever. After issuereport.MaxSentFailCount
// consecutive SaveSent failures, the till gives up on local retention and
// discards the bundle — it was already delivered to the cloud (idempotent
// there), so nothing is lost except this till's own "sent" record of it.
func TestUploadPendingIssueReportsDiscardsAfterSaveSentFailureCap(t *testing.T) {
	withTempPendingDir(t)
	brokenDB := testDB(t) // settings table only — SaveSent always fails
	id, err := issuereport.Save("record must not loop forever", "", []byte("audio"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	var uploadCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploadCount++
		_ = r.ParseMultipartForm(10 << 20)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for i := 0; i < issuereport.MaxSentFailCount; i++ {
		uploadPendingIssueReports(context.Background(), registeredCfg(srv.URL), brokenDB)
	}

	if uploadCount != issuereport.MaxSentFailCount {
		t.Fatalf("cloud upload called %d times, want exactly %d (must stop re-uploading once it gives up)", uploadCount, issuereport.MaxSentFailCount)
	}
	remaining, err := issuereport.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("bundle %q still pending after %d failed SaveSent attempts, want it discarded", id, issuereport.MaxSentFailCount)
	}

	// One more tick must be a true no-op: nothing left to upload, no growth.
	uploadPendingIssueReports(context.Background(), registeredCfg(srv.URL), brokenDB)
	if uploadCount != issuereport.MaxSentFailCount {
		t.Fatalf("cloud upload called %d times after an extra tick, want it to stay at %d", uploadCount, issuereport.MaxSentFailCount)
	}
}

// A bundle whose cloud upload keeps FAILING (not the SaveSent step) must keep
// retrying unboundedly — the cap above applies only to the SaveSent-failure
// path. This is the offline-first guarantee: a till with no connectivity for
// days must keep trying once it reconnects, not give up after N ticks.
func TestUploadPendingIssueReportsUploadFailureIsNeverCapped(t *testing.T) {
	withTempPendingDir(t)
	d := openMigratedDB(t, "issue_reports_uncapped.db")
	id, err := issuereport.Save("cloud is unreachable", "", []byte("audio"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	for i := 0; i < issuereport.MaxSentFailCount+3; i++ {
		uploadPendingIssueReports(context.Background(), registeredCfg(srv.URL), d.DB)
	}

	remaining, err := issuereport.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Meta.ID != id {
		t.Fatalf("bundle must still be pending after upload failures alone (never capped): %+v", remaining)
	}
	if remaining[0].Meta.SentFailCount != 0 {
		t.Fatalf("SentFailCount = %d, want 0 — an upload failure must never touch the SaveSent failure counter", remaining[0].Meta.SentFailCount)
	}
}

// ut-docs#637: a bundle whose upload fails because this till isn't
// registered must be classified as UploadFailReasonNotRegistered — the one
// reason that can't self-resolve by waiting, so /my-reports flags it
// immediately rather than waiting out UploadFailingThreshold.
func TestUploadPendingIssueReportsClassifiesNotRegistered(t *testing.T) {
	withTempPendingDir(t)
	d := openMigratedDB(t, "issue_reports_unregistered.db")
	id, err := issuereport.Save("till never enrolled", "", []byte("audio"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// An empty config's Marketplace fields are all unset — uploadIssueReport
	// returns errNotRegistered before any network call.
	uploadPendingIssueReports(context.Background(), &config.Config{}, d.DB)

	remaining, err := issuereport.Pending()
	if err != nil || len(remaining) != 1 || remaining[0].Meta.ID != id {
		t.Fatalf("Pending: %v (%d)", err, len(remaining))
	}
	if remaining[0].Meta.UploadFailReason != issuereport.UploadFailReasonNotRegistered {
		t.Fatalf("UploadFailReason = %q, want %q", remaining[0].Meta.UploadFailReason, issuereport.UploadFailReasonNotRegistered)
	}
	if remaining[0].Meta.UploadFailCount != 1 {
		t.Fatalf("UploadFailCount = %d, want 1", remaining[0].Meta.UploadFailCount)
	}
}

// A generic upload failure (server error, network trouble) classifies as
// UploadFailReasonOther and accumulates across ticks — the count
// /my-reports compares against issuereport.UploadFailingThreshold.
func TestUploadPendingIssueReportsClassifiesOtherAndAccumulates(t *testing.T) {
	withTempPendingDir(t)
	d := openMigratedDB(t, "issue_reports_other_fail.db")
	id, err := issuereport.Save("cloud keeps 500ing", "", []byte("audio"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	for i := 0; i < 3; i++ {
		uploadPendingIssueReports(context.Background(), registeredCfg(srv.URL), d.DB)
	}

	remaining, err := issuereport.Pending()
	if err != nil || len(remaining) != 1 || remaining[0].Meta.ID != id {
		t.Fatalf("Pending: %v (%d)", err, len(remaining))
	}
	if remaining[0].Meta.UploadFailReason != issuereport.UploadFailReasonOther {
		t.Fatalf("UploadFailReason = %q, want %q", remaining[0].Meta.UploadFailReason, issuereport.UploadFailReasonOther)
	}
	if remaining[0].Meta.UploadFailCount != 3 {
		t.Fatalf("UploadFailCount = %d, want 3 (one per tick)", remaining[0].Meta.UploadFailCount)
	}
}

// A bundle that eventually uploads successfully is discarded outright — its
// UploadFailCount from earlier failed ticks doesn't linger anywhere to be
// misread, because there's no bundle left to read it from.
func TestUploadPendingIssueReportsSuccessAfterFailuresLeavesNoUploadFailCount(t *testing.T) {
	withTempPendingDir(t)
	d := openMigratedDB(t, "issue_reports_recovers.db")
	id, err := issuereport.Save("flaky then fine", "", []byte("audio"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = r.ParseMultipartForm(10 << 20)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	uploadPendingIssueReports(context.Background(), registeredCfg(srv.URL), d.DB)
	uploadPendingIssueReports(context.Background(), registeredCfg(srv.URL), d.DB)
	fail.Store(false)
	uploadPendingIssueReports(context.Background(), registeredCfg(srv.URL), d.DB)

	remaining, err := issuereport.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("bundle %q still pending after a successful upload, want it discarded: %+v", id, remaining)
	}
	sent, err := data.NewIssueReportsRepo(d.DB).ListSent(context.Background(), 10)
	if err != nil || len(sent) != 1 || sent[0].ID != id {
		t.Fatalf("ListSent: %v %+v", err, sent)
	}
}

// Pending() itself erroring (not just "no bundles yet") must not panic the
// tick loop — uploadPendingIssueReports logs and returns.
func TestUploadPendingIssueReportsPendingListError(t *testing.T) {
	orig := issuereport.PendingDir
	// A regular file where a directory is expected makes os.ReadDir fail with
	// something other than the plain "does not exist" case Pending() already
	// handles.
	f := t.TempDir() + "/not-a-dir"
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	issuereport.PendingDir = f
	t.Cleanup(func() { issuereport.PendingDir = orig })

	// Must not panic; nothing to assert on the (empty) cloud side.
	uploadPendingIssueReports(context.Background(), registeredCfg("http://unused.example"), testDB(t))
}

// pullIssueReportStatuses pulls the cloud's per-report statuses down and
// applies them to the retained local rows — matched by the till's own report
// id, which is the correlation key (the cloud echoes it back verbatim).
func TestPullIssueReportStatusesUpdatesLocalRows(t *testing.T) {
	d := openMigratedDB(t, "issue_reports_pull.db")
	repo := data.NewIssueReportsRepo(d.DB)
	ctx := context.Background()
	for i, id := range []string{"rep-filed", "rep-transcribing", "rep-untouched"} {
		if err := repo.SaveSent(ctx, data.SentReport{
			ID:         id,
			CapturedAt: time.Date(2026, 8, 1+i, 0, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("SaveSent %s: %v", id, err)
		}
	}

	var gotPath, gotAuth, gotStoreID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotStoreID = r.URL.Query().Get("store_id")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"store_id": "store-1",
				"reports": []map[string]any{
					{"id": "rep-filed", "status": "filed", "github_issue_url": "https://github.com/universaltill/ut-docs/issues/999", "captured_at": "2026-08-01T00:00:00Z"},
					{"id": "rep-transcribing", "status": "transcribing", "github_issue_url": "", "captured_at": "2026-08-02T00:00:00Z"},
					// An id this till never retained — must be silently skipped.
					{"id": "rep-from-another-life", "status": "discarded", "github_issue_url": "", "captured_at": "2020-01-01T00:00:00Z"},
				},
			},
			"error": nil,
		})
	}))
	defer srv.Close()

	pullIssueReportStatuses(context.Background(), registeredCfg(srv.URL), d.DB)

	if gotPath != "/v1/stores/issue-reports" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok-1" || gotStoreID != "store-1" {
		t.Fatalf("auth = %q store_id = %q", gotAuth, gotStoreID)
	}

	sent, err := repo.ListSent(ctx, 10)
	if err != nil {
		t.Fatalf("ListSent: %v", err)
	}
	byID := map[string]data.SentReport{}
	for _, r := range sent {
		byID[r.ID] = r
	}
	if len(byID) != 3 {
		t.Fatalf("retained rows = %d, want 3 (the unknown cloud id must not create one)", len(byID))
	}
	if byID["rep-filed"].Status != "filed" || byID["rep-filed"].GithubIssueURL != "https://github.com/universaltill/ut-docs/issues/999" {
		t.Fatalf("rep-filed not updated: %+v", byID["rep-filed"])
	}
	if byID["rep-transcribing"].Status != "transcribing" {
		t.Fatalf("rep-transcribing not updated: %+v", byID["rep-transcribing"])
	}
	if byID["rep-untouched"].Status != "sent" {
		t.Fatalf("rep-untouched must keep its local-only status: %+v", byID["rep-untouched"])
	}
}

// Best-effort contract: an unregistered store, a 500, or an unparsable body
// must all be silent no-ops — no panic, no error propagated, local rows
// untouched.
func TestPullIssueReportStatusesBestEffortNoops(t *testing.T) {
	d := openMigratedDB(t, "issue_reports_pull_noop.db")
	repo := data.NewIssueReportsRepo(d.DB)
	ctx := context.Background()
	if err := repo.SaveSent(ctx, data.SentReport{
		ID:         "rep-1",
		CapturedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("SaveSent: %v", err)
	}
	assertUntouched := func(label string) {
		t.Helper()
		sent, err := repo.ListSent(ctx, 10)
		if err != nil || len(sent) != 1 || sent[0].Status != "sent" {
			t.Fatalf("%s: local row must be untouched: %v %+v", label, err, sent)
		}
	}

	// Not registered — must return before any network call.
	pullIssueReportStatuses(context.Background(), &config.Config{}, d.DB)
	assertUntouched("unregistered")

	// Server 500s.
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv500.Close()
	pullIssueReportStatuses(context.Background(), registeredCfg(srv500.URL), d.DB)
	assertUntouched("500")

	// Body doesn't parse.
	srvGarbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srvGarbage.Close()
	pullIssueReportStatuses(context.Background(), registeredCfg(srvGarbage.URL), d.DB)
	assertUntouched("garbage body")
}

func TestAttachFileMultipartFormWritesRealBytes(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("note", "", []byte("hello-bytes"), nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, _ := issuereport.Pending()
	var buf strings.Builder
	w := multipart.NewWriter(&buf)
	if err := attachFile(w, "audio", "audio.webm", bundles[0].AudioPath); err != nil {
		t.Fatalf("attachFile: %v", err)
	}
	w.Close()
	if !strings.Contains(buf.String(), "hello-bytes") {
		t.Fatal("attached file content missing from multipart body")
	}
}
