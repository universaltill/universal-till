package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/plugins"
)

// Plugin view uploads (ADR-0121 §7, ut-docs#3793): a form's file field is
// streamed to a staged temp file the plugin reads by token; the file never
// outlives the ask (or the job) that received it.

const uploadRoute = "/plugin/views/upload"

var uploadHandleRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// pngBytes is a PNG signature plus padding: http.DetectContentType reads
// it as image/png.
func pngBytes(n int) []byte {
	b := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, max(n-8, 0))...)
	return b[:n]
}

// isolateTemp scopes os.TempDir() (handler's staging and any stdlib
// multipart spool) to this test.
func isolateTemp(t *testing.T) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
}

func stagedUploads(t *testing.T) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(os.TempDir(), "ut-view-upload-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func assertNoStaged(t *testing.T) {
	t.Helper()
	// Cleanup at the end of an ask can run just after the response; give
	// it a moment, never longer.
	deadline := time.Now().Add(2 * time.Second)
	for len(stagedUploads(t)) > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if left := stagedUploads(t); len(left) > 0 {
		t.Fatalf("staged upload temp files left behind: %v", left)
	}
}

type uploadPart struct {
	field, filename string
	content         []byte
}

func multipartPost(t *testing.T, fields map[string]string, files []uploadPart) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		h := make(textproto.MIMEHeader)
		// Quoted by hand (only '\\' and '"' escaped), as a browser sends it:
		// %q would turn a control character into a Go escape.
		q := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
		h.Set("Content-Disposition", `form-data; name="`+q.Replace(f.field)+`"; filename="`+q.Replace(f.filename)+`"`)
		h.Set("Content-Type", "application/octet-stream")
		w, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(f.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func (h *viewHarness) doMultipart(path string, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// addUploadEntry adds a view entry declaring upload_max_mb.
func addUploadEntry(t *testing.T, h *viewHarness, maxMB int) {
	t.Helper()
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label,config_json) VALUES ('v5',?,'page','upload',?,'Upload',?)`,
		viewPluginID, uploadRoute, fmt.Sprintf(`{"view":"views.upload","upload_max_mb":%d}`, maxMB)); err != nil {
		t.Fatal(err)
	}
}

const savedDoc = `{"document":{"version":1,"components":[{"type":"notice","level":"info","text":{"literal":"Saved"}}]}}`

type seenUpload struct {
	Field       string `json:"field"`
	Handle      string `json:"handle"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

func lastUploads(t *testing.T, pay map[string]any) []seenUpload {
	t.Helper()
	raw, err := json.Marshal(pay["upload_handles"])
	if err != nil {
		t.Fatal(err)
	}
	var out []seenUpload
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		t.Fatalf("upload_handles = %s, want an array", raw)
	}
	return out
}

func TestPluginViewUpload_PayloadAndCleanupAfterAsk_3793(t *testing.T) {
	isolateTemp(t)
	h := newViewHarness(t)
	addUploadEntry(t, h, 1)
	stagedDuringAsk := -1
	h.mu.Lock()
	h.answer = func(plugins.Event) (json.RawMessage, error) {
		stagedDuringAsk = len(stagedUploads(t))
		return json.RawMessage(savedDoc), nil
	}
	h.mu.Unlock()

	content := pngBytes(5000)
	body, ct := multipartPost(t, map[string]string{"_action": "identify", "note": "tea"},
		[]uploadPart{{"photo", "C:\\fakepath\\ca\u0085t.png", content}, {"empty", "", nil}})
	rec := h.doMultipart(uploadRoute, body, ct)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Saved") {
		t.Fatalf("POST = %d:\n%s", rec.Code, rec.Body.String())
	}
	if stagedDuringAsk != 1 {
		t.Fatalf("staged files during the ask = %d, want 1", stagedDuringAsk)
	}
	if f, _ := h.lastPay["form"].(map[string]any); f["note"] != "tea" || len(f) != 1 {
		t.Fatalf("form = %v, want only note (file parts never in form)", h.lastPay["form"])
	}
	ups := lastUploads(t, h.lastPay)
	if len(ups) != 1 {
		t.Fatalf("upload_handles = %+v, want the one non-empty file", ups)
	}
	u := ups[0]
	if u.Field != "photo" || !uploadHandleRe.MatchString(u.Handle) || u.Filename != "cat.png" || u.Size != int64(len(content)) || u.ContentType != "image/png" {
		t.Fatalf("upload = %+v", u)
	}
	assertNoStaged(t)

	// An answer core cannot use still releases the upload.
	h.answerWith(`{"document":`)
	body, ct = multipartPost(t, map[string]string{"_action": "identify"}, []uploadPart{{"photo", "a.png", content}})
	assertActionFailed(t, h.doMultipart(uploadRoute, body, ct), http.StatusBadGateway, "plugin.view.action_failed")
	assertNoStaged(t)
}

func TestPluginViewUpload_OverMaxIsInvalid_3793(t *testing.T) {
	isolateTemp(t)
	h := newViewHarness(t)
	addUploadEntry(t, h, 1)
	stagedDuringAsk := -1
	h.mu.Lock()
	h.answer = func(plugins.Event) (json.RawMessage, error) {
		stagedDuringAsk = len(stagedUploads(t))
		return json.RawMessage(savedDoc), nil
	}
	h.mu.Unlock()
	body, ct := multipartPost(t, map[string]string{"_action": "identify"},
		[]uploadPart{{"photo", "big.png", pngBytes(1<<20 + 1)}, {"small", "s.png", pngBytes(100)}})
	rec := h.doMultipart(uploadRoute, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d:\n%s", rec.Code, rec.Body.String())
	}
	inv, _ := h.lastPay["invalid"].([]any)
	if len(inv) != 1 || inv[0] != "photo" {
		t.Fatalf("invalid = %v, want [photo]", h.lastPay["invalid"])
	}
	ups := lastUploads(t, h.lastPay)
	if len(ups) != 1 || ups[0].Field != "small" {
		t.Fatalf("upload_handles = %+v, want only the file within the cap", ups)
	}
	if stagedDuringAsk != 1 {
		t.Fatalf("staged during the ask = %d, want 1 (the oversize file removed at once)", stagedDuringAsk)
	}
	assertNoStaged(t)
}

func TestPluginViewUpload_Refusals_3793(t *testing.T) {
	isolateTemp(t)
	h := newViewHarness(t)
	addUploadEntry(t, h, 1)
	h.answerWith(savedDoc)
	small := pngBytes(64)

	t.Run("more than four files", func(t *testing.T) {
		h.lastEv = plugins.Event{}
		var files []uploadPart
		for i := 0; i < 5; i++ {
			files = append(files, uploadPart{fmt.Sprintf("f%d", i), "x.png", small})
		}
		body, ct := multipartPost(t, map[string]string{"_action": "identify"}, files)
		assertActionFailed(t, h.doMultipart(uploadRoute, body, ct), http.StatusBadRequest, "plugin.view.action_failed")
		if h.lastEv.Type != "" {
			t.Fatal("the plugin was asked")
		}
		assertNoStaged(t)
	})
	t.Run("entry without upload_max_mb", func(t *testing.T) {
		h.lastEv = plugins.Event{}
		body, ct := multipartPost(t, map[string]string{"_action": "save"}, []uploadPart{{"photo", "x.png", small}})
		assertActionFailed(t, h.doMultipart("/plugin/views", body, ct), http.StatusBadRequest, "plugin.view.action_failed")
		if h.lastEv.Type != "" {
			t.Fatal("the plugin was asked")
		}
		assertNoStaged(t)
		// No file chosen is not an upload: the post goes through.
		body, ct = multipartPost(t, map[string]string{"_action": "save", "note": "n"}, []uploadPart{{"photo", "", nil}})
		if rec := h.doMultipart("/plugin/views", body, ct); rec.Code != http.StatusOK {
			t.Fatalf("multipart post without a file = %d", rec.Code)
		}
	})
	t.Run("bad file field name", func(t *testing.T) {
		body, ct := multipartPost(t, map[string]string{"_action": "identify"}, []uploadPart{{"../x", "x.png", small}})
		assertActionFailed(t, h.doMultipart(uploadRoute, body, ct), http.StatusBadRequest, "plugin.view.action_failed")
		assertNoStaged(t)
	})
	t.Run("body past the whole-post bound", func(t *testing.T) {
		// 5 MB on a 1 MB entry is past MaxFormBytes + 4*max + slack: the
		// http.MaxBytesReader trips while the oversize file is drained.
		h.lastEv = plugins.Event{}
		body, ct := multipartPost(t, map[string]string{"_action": "identify"}, []uploadPart{{"photo", "x.png", pngBytes(5 << 20)}})
		assertActionFailed(t, h.doMultipart(uploadRoute, body, ct), http.StatusBadRequest, "plugin.view.action_failed")
		if h.lastEv.Type != "" {
			t.Fatal("the plugin was asked")
		}
		assertNoStaged(t)
	})
	t.Run("text parts over the form cap", func(t *testing.T) {
		body, ct := multipartPost(t, map[string]string{"_action": "identify", "note": strings.Repeat("a", 70<<10)}, []uploadPart{{"photo", "x.png", small}})
		assertActionFailed(t, h.doMultipart(uploadRoute, body, ct), http.StatusBadRequest, "plugin.view.action_failed")
		assertNoStaged(t)
	})
}

// The multipart body is streamed (r.MultipartReader), never spooled by
// mime/multipart's ReadForm (ParseMultipartForm/FormFile spool past 32 MiB).
func TestPluginViewUpload_NoStdlibSpool_3793(t *testing.T) {
	isolateTemp(t)
	h := newViewHarness(t)
	addUploadEntry(t, h, 32)
	sawSpool := false
	h.mu.Lock()
	h.answer = func(plugins.Event) (json.RawMessage, error) {
		m, _ := filepath.Glob(filepath.Join(os.TempDir(), "multipart-*"))
		sawSpool = len(m) > 0
		return json.RawMessage(savedDoc), nil
	}
	h.mu.Unlock()
	body, ct := multipartPost(t, map[string]string{"_action": "identify"},
		[]uploadPart{{"a", "a.bin", bytes.Repeat([]byte("a"), 17<<20)}, {"b", "b.bin", bytes.Repeat([]byte("b"), 17<<20)}})
	rec := h.doMultipart(uploadRoute, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d", rec.Code)
	}
	if len(lastUploads(t, h.lastPay)) != 2 {
		t.Fatalf("upload_handles = %v", h.lastPay["upload_handles"])
	}
	m, _ := filepath.Glob(filepath.Join(os.TempDir(), "multipart-*"))
	if sawSpool || len(m) > 0 {
		t.Fatal("the upload was spooled by mime/multipart too")
	}
	assertNoStaged(t)
}

func TestPluginViewUpload_RenderFileField_3793(t *testing.T) {
	h := newViewHarness(t)
	addUploadEntry(t, h, 4)
	fileDoc := `{"document":{"version":1,"components":[{"type":"form","action":"identify","submit":{"literal":"Go"},"fields":[
		{"name":"photo","label":{"literal":"Photo"},"kind":"file","required":true}]}]}}`
	h.answerWith(fileDoc)
	rec := h.do(http.MethodGet, uploadRoute, nil, false)
	body := rec.Body.String()
	for _, want := range []string{`enctype="multipart/form-data"`, `hx-encoding="multipart/form-data"`, `<input type="file" name="photo" required>`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	// The same document from an entry that declared no upload max.
	assertUnavailable(t, h.do(http.MethodGet, "/plugin/views", nil, false), http.StatusOK)
}

// A job started by the action owns its uploads until it ends; then they go.
func TestPluginViewUpload_JobOwnsUploadsUntilItEnds_3793(t *testing.T) {
	isolateTemp(t)
	h := newJobHarness(t)
	addUploadEntry(t, h.viewHarness, 1)
	release := make(chan struct{})
	started := make(chan struct{})
	h.onJob(func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(savedDoc), nil
	})
	body, ct := multipartPost(t, map[string]string{"_action": "identify"}, []uploadPart{{"photo", "p.png", pngBytes(300)}})
	rec := h.doMultipart(uploadRoute, body, ct)
	m := jobIDRe.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil {
		t.Fatalf("job action = %d:\n%s", rec.Code, rec.Body.String())
	}
	<-started
	if n := len(stagedUploads(t)); n != 1 {
		t.Fatalf("staged while the job runs = %d, want 1", n)
	}
	h.jobMu.Lock()
	ups := lastUploads(t, h.jobPayload)
	h.jobMu.Unlock()
	if len(ups) != 1 || ups[0].Field != "photo" {
		t.Fatalf("job payload upload_handles = %+v", ups)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for stillRunning(h.poll(uploadRoute, m[1])) {
		if time.Now().After(deadline) {
			t.Fatal("job never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertNoStaged(t)
}

// A job refused as busy releases the uploads at once.
func TestPluginViewUpload_BusyJobReleases_3793(t *testing.T) {
	isolateTemp(t)
	h := newJobHarness(t)
	addUploadEntry(t, h.viewHarness, 1)
	pluginJobCaps = func() (int, int) { return 0, 0 }
	body, ct := multipartPost(t, map[string]string{"_action": "identify"}, []uploadPart{{"photo", "p.png", pngBytes(300)}})
	if rec := h.doMultipart(uploadRoute, body, ct); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("busy job = %d, want 429", rec.Code)
	}
	assertNoStaged(t)
}

func TestPluginViewUpload_FilenameLabel_3793(t *testing.T) {
	for in, want := range map[string]string{
		`C:\photos\tea.png`:      "tea.png",
		"a/b/c.jpg":              "c.jpg",
		"bad\x00\x1fname.png":    "badname.png",
		"inv\u202egnp.exe":       "invgnp.exe",
		"zero\u200dwidth.png":    "zerowidth.png",
		strings.Repeat("é", 200): strings.Repeat("é", 127),
	} {
		if got := uploadFilename(in); got != want {
			t.Errorf("uploadFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// A file posted from a content slot panel runs under ui:slot:<slot> alone:
// the plugin holds no ui:page (ut-docs#3963).
func TestPluginViewUpload_IntoSlotPanel_3963(t *testing.T) {
	isolateTemp(t)
	t.Setenv("UT_AUTH", "off") // the slot's host gate has its own test
	h := newViewHarness(t)
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label,config_json) VALUES ('v6',?,'page','slotup',?,'Slot upload',?)`,
		viewPluginID, uploadRoute, `{"view":"views.upload","content_slot":"reports.panels","upload_max_mb":1}`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.Db.Exec(`UPDATE plugin_permissions SET granted = 0 WHERE plugin_id = ? AND permission = 'ui:page'`, viewPluginID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('slotup',?,'ui:slot:reports.panels',1)`, viewPluginID); err != nil {
		t.Fatal(err)
	}
	h.answerWith(`{"document":{"version":1,"components":[{"type":"notice","level":"info","text":{"literal":"Saved"}},{"type":"button","action":"again","label":{"literal":"Again"}}]}}`)

	body, ct := multipartPost(t, map[string]string{"_action": "identify"}, []uploadPart{{"photo", "cat.png", pngBytes(2000)}})
	req := httptest.NewRequest(http.MethodPost, uploadRoute, body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "plugin-slot-reports-panels-0")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Saved") || !strings.Contains(rec.Body.String(), `hx-target="#plugin-slot-reports-panels-0"`) {
		t.Fatalf("POST = %d:\n%s", rec.Code, rec.Body.String())
	}
	ups := lastUploads(t, h.lastPay)
	if len(ups) != 1 || ups[0].Field != "photo" || ups[0].Filename != "cat.png" || ups[0].Size != 2000 {
		t.Fatalf("upload_handles = %+v", ups)
	}
	assertNoStaged(t)
}
