package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/ai"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// The sell screen's catalog.identify seam (ADR-0121 §7, ut-docs#3873): the
// camera overlay stages the photo, runs catalog.identify as a job on the
// lexically first subscribed plugin, and renders its suggestions as
// /api/pos/scan buttons.

var identifyPollRe = regexp.MustCompile(`hx-get="/api/pos/identify/plugin\?_job=([0-9a-f]{32})"`)

type identifyHarness struct {
	*viewHarness
	jmu     sync.Mutex
	handler func(ctx context.Context, ev plugins.Event) (json.RawMessage, error)
	payload map[string]any
}

func newIdentifyHarness(t *testing.T) *identifyHarness {
	t.Helper()
	h := &identifyHarness{viewHarness: newViewHarness(t)}
	isolateTemp(t)
	origReg, origWatch, origCaps := pluginJobs, pluginJobWatchEvery, pluginJobCaps
	pluginJobs = newPluginJobRegistry()
	pluginJobWatchEvery = 10 * time.Millisecond
	pluginJobCaps = func() (int, int) { return plugins.JobCaps("linux") }
	t.Cleanup(func() { pluginJobs, pluginJobWatchEvery, pluginJobCaps = origReg, origWatch, origCaps })
	registerPluginIdentify(h.mux, h.d)
	addIdentifyHook(t, h, viewPluginID)
	if _, err := plugins.SharedBus(h.d.Db).SubscribeWithHandler(t.Context(), viewPluginID, []string{identifyEvent},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			h.jmu.Lock()
			h.payload = nil
			_ = json.Unmarshal(ev.Payload, &h.payload)
			f := h.handler
			h.jmu.Unlock()
			if f == nil {
				return nil, nil
			}
			return f(ctx, ev)
		}); err != nil {
		t.Fatalf("subscribe %s: %v", identifyEvent, err)
	}
	return h
}

// addIdentifyHook declares the plugin's catalog.identify hook (manifest
// "hooks"), as a real identify plugin must.
func addIdentifyHook(t *testing.T, h *identifyHarness, pluginID string) {
	t.Helper()
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_hooks(id,plugin_id,event,action,is_active) VALUES(?,?,?,'identify',1)`, pluginID+"-hidentify", pluginID, identifyEvent); err != nil {
		t.Fatal(err)
	}
}

func (h *identifyHarness) answer(raw string) {
	h.jmu.Lock()
	h.handler = func(context.Context, plugins.Event) (json.RawMessage, error) { return json.RawMessage(raw), nil }
	h.jmu.Unlock()
}

func (h *identifyHarness) post(photo []byte) *httptest.ResponseRecorder {
	h.t.Helper()
	body, ct := multipartPost(h.t, nil, []uploadPart{{field: "photo", filename: "capture.jpg", content: photo}})
	return h.doMultipart(identifyRoute, body, ct)
}

func (h *identifyHarness) pollUntilDone(id string) *httptest.ResponseRecorder {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec := h.do(http.MethodGet, identifyRoute+"?_job="+id, nil, true)
		if rec.Code != http.StatusNoContent && !strings.Contains(rec.Body.String(), "data-identify-poll") {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatal("identify job never finished")
	return nil
}

// jpegBytes: a JPEG SOI/APP0 header plus padding, sniffed as image/jpeg.
func jpegBytes(n int) []byte {
	b := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), bytes.Repeat([]byte{0}, n)...)
	return b[:n]
}

func (h *identifyHarness) start() string {
	h.t.Helper()
	rec := h.post(jpegBytes(2048))
	m := identifyPollRe.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil {
		h.t.Fatalf("identify start = %d, no poll in:\n%s", rec.Code, rec.Body.String())
	}
	return m[1]
}

func TestPluginIdentify_PollThenSuggestions_3873(t *testing.T) {
	h := newIdentifyHarness(t)
	if _, err := h.d.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES ('itm-tea','TEA-1','Tea',250,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.Db.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img-tea','itm-tea','/public/assets/category-icons/coffee.svg','thumbnail')`); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	h.jmu.Lock()
	h.handler = func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		<-release
		return json.RawMessage(`{"document":{"version":1,"components":[
			{"type":"text","text":{"literal":"Best guesses"}},
			{"type":"suggestions","items":[
				{"label":{"literal":"Tea <b>"},"detail":{"key":"plugin.views.hello"},"effect":{"add_to_basket":{"sku":"TEA-1","qty":2}}},
				{"label":{"literal":"Unknown"},"effect":{"add_to_basket":{"sku":"NOPE"}}}]}]}}`), nil
	}
	h.jmu.Unlock()

	rec := h.post(jpegBytes(4096))
	body := rec.Body.String()
	m := identifyPollRe.FindStringSubmatch(body)
	if rec.Code != http.StatusOK || m == nil {
		t.Fatalf("POST = %d, want the poll fragment:\n%s", rec.Code, body)
	}
	id := m[1]
	for _, want := range []string{`data-identify-poll`, `hx-trigger="every 1s"`, `hx-swap="outerHTML"`, `aria-live="polite"`,
		template.HTMLEscapeString(httpx.T("en", "plugin.job.running"))} {
		if !strings.Contains(body, want) {
			t.Errorf("poll missing %q in:\n%s", want, body)
		}
	}
	// The job got the photo as an upload handle, plus the locale and job id.
	deadline := time.Now().Add(2 * time.Second)
	var pay map[string]any
	for time.Now().Before(deadline) {
		h.jmu.Lock()
		pay = h.payload
		h.jmu.Unlock()
		if pay != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ups := lastUploads(t, pay)
	if len(ups) != 1 || ups[0].Field != "photo" || ups[0].Filename != "capture.jpg" || ups[0].Size != 4096 || ups[0].ContentType != "image/jpeg" || !uploadHandleRe.MatchString(ups[0].Handle) {
		t.Fatalf("upload_handles = %+v", ups)
	}
	if pay["locale"] != "en" || pay["job_id"] != id {
		t.Fatalf("payload = %v", pay)
	}
	if len(stagedUploads(t)) != 1 {
		t.Fatalf("the job must own the staged photo while it runs: %v", stagedUploads(t))
	}
	// Running, unchanged: 204 for htmx.
	if rec := h.do(http.MethodGet, identifyRoute+"?_job="+id, nil, true); rec.Code != http.StatusNoContent {
		t.Fatalf("unchanged running poll = %d, want 204", rec.Code)
	}
	close(release)
	rec = h.pollUntilDone(id)
	body = rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("done poll = %d:\n%s", rec.Code, body)
	}
	for _, want := range []string{
		`hx-post="/api/pos/scan"`,
		`hx-vals='{&#34;code&#34;:&#34;TEA-1&#34;,&#34;qty&#34;:2}'`,
		`hx-vals='{&#34;code&#34;:&#34;NOPE&#34;,&#34;qty&#34;:1}'`,
		`hx-target="#basket"`,
		`hx-swap="outerHTML"`,
		`hx-sync="#basket:replace"`,
		`class="btn ai-match"`,
		`data-identify-pick`,
		`title="` + template.HTMLEscapeString(httpx.T("en", "suggest.add")) + `"`,
		`Tea &lt;b&gt;`,
		`plugin.views.hello`,
		`Best guesses`,
		`/public/assets/category-icons/coffee.svg`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("result missing %q in:\n%s", want, body)
		}
	}
	if strings.Count(body, "<img") != 1 {
		t.Errorf("only the catalog item with a photo shows one:\n%s", body)
	}
	assertNoStaged(t)
	// The result is handed out once.
	assertNotice(t, h.do(http.MethodGet, identifyRoute+"?_job="+id, nil, true), "plugin.job.gone")
}

func TestPluginIdentify_NoSuggestions_3873(t *testing.T) {
	h := newIdentifyHarness(t)
	h.answer(`{"document":{"version":1,"components":[{"type":"notice","level":"info","text":{"literal":"Too dark"}}]}}`)
	rec := h.pollUntilDone(h.start())
	if !strings.Contains(rec.Body.String(), "Too dark") {
		t.Errorf("the document's notice is missing:\n%s", rec.Body.String())
	}
	assertNotice(t, rec, "ai.identify.no_match")
	if strings.Contains(rec.Body.String(), "/api/pos/scan") {
		t.Error("no suggestions, but a scan button rendered")
	}
}

// apply_fields is the item form's effect: refused at the sell seam, so
// the job fails and the overlay says so.
func TestPluginIdentify_RefusedAnswers_3873(t *testing.T) {
	for name, raw := range map[string]string{
		"apply_fields": `{"document":{"version":1,"components":[{"type":"suggestions","items":[{"label":{"literal":"x"},"effect":{"apply_fields":{"name":"x"}}}]}]}}`,
		"form in seam": `{"document":{"version":1,"components":[{"type":"form","action":"go","submit":{"literal":"s"},"fields":[]}]}}`,
		"redirect":     `{"redirect":"/plugin/views"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newIdentifyHarness(t)
			h.answer(raw)
			rec := h.pollUntilDone(h.start())
			assertNotice(t, rec, "ai.identify.error")
			if strings.Contains(rec.Body.String(), "/api/pos/scan") || rec.Header().Get("HX-Redirect") != "" {
				t.Fatalf("refused answer rendered something:\n%s", rec.Body.String())
			}
		})
	}
}

func TestPluginIdentify_NoPlugin404_3873(t *testing.T) {
	h := newIdentifyHarness(t)
	orig := identifyPluginID
	identifyPluginID = func(context.Context, *common.Deps) string { return "" }
	t.Cleanup(func() { identifyPluginID = orig })
	rec := h.post(jpegBytes(1024))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("no plugin: status = %d, want 404", rec.Code)
	}
	assertNotice(t, rec, "plugin.view.unavailable")
	assertNoStaged(t)
}

func TestPluginIdentify_UploadRefusals_3873(t *testing.T) {
	h := newIdentifyHarness(t)
	h.answer(`{"document":{"version":1,"components":[]}}`)
	cases := map[string]struct {
		fields map[string]string
		files  []uploadPart
		want   int
	}{
		"not an image":     {files: []uploadPart{{"photo", "x.jpg", []byte("%PDF-1.4 not a photo at all")}}, want: http.StatusBadRequest},
		"gif":              {files: []uploadPart{{"photo", "x.gif", []byte("GIF89a......")}}, want: http.StatusBadRequest},
		"no file":          {want: http.StatusBadRequest},
		"wrong field":      {files: []uploadPart{{"image", "x.jpg", jpegBytes(100)}}, want: http.StatusBadRequest},
		"two files":        {files: []uploadPart{{"photo", "a.jpg", jpegBytes(100)}, {"photo", "b.jpg", jpegBytes(100)}}, want: http.StatusBadRequest},
		"extra text part":  {fields: map[string]string{"note": "x"}, files: []uploadPart{{"photo", "a.jpg", jpegBytes(100)}}, want: http.StatusBadRequest},
		"over 8 MiB":       {files: []uploadPart{{"photo", "big.jpg", jpegBytes(identifyMaxPhotoBytes + 1)}}, want: http.StatusRequestEntityTooLarge},
		"empty photo part": {files: []uploadPart{{"photo", "a.jpg", nil}}, want: http.StatusBadRequest},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			body, ct := multipartPost(t, c.fields, c.files)
			rec := h.doMultipart(identifyRoute, body, ct)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d:\n%s", rec.Code, c.want, rec.Body.String())
			}
			assertNotice(t, rec, "ai.identify.error")
			assertNoStaged(t)
		})
	}
	// Not multipart at all.
	rec := h.do(http.MethodPost, identifyRoute, map[string][]string{"photo": {"x"}}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("url-encoded post = %d, want 400", rec.Code)
	}
	// PNG and WebP are photos too.
	for name, b := range map[string][]byte{"png": pngBytes(512), "webp": append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 64)...)} {
		body, ct := multipartPost(t, nil, []uploadPart{{"photo", "p", b}})
		if rec := h.doMultipart(identifyRoute, body, ct); rec.Code != http.StatusOK {
			t.Errorf("%s photo refused: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// A poll is answered for the plugin that started the job, without
// re-resolving (and re-checking) the identify plugin every second.
func TestPluginIdentify_PollUsesTheJobsPlugin_3873(t *testing.T) {
	h := newIdentifyHarness(t)
	h.answer(`{"document":{"version":1,"components":[{"type":"suggestions","items":[{"label":{"literal":"Tea"},"effect":{"add_to_basket":{"sku":"TEA-1"}}}]}]}}`)
	id := h.start()
	orig := identifyPluginID
	identifyPluginID = func(context.Context, *common.Deps) string { panic("poll re-resolved the identify plugin") }
	t.Cleanup(func() { identifyPluginID = orig })
	rec := h.pollUntilDone(id)
	if !strings.Contains(rec.Body.String(), "TEA-1") {
		t.Fatalf("poll = %d, want the suggestion:\n%s", rec.Code, rec.Body.String())
	}
}

// A new capture replaces this seam's earlier, unpolled job (a Retake or
// Close while the photo was still uploading): on a tablet (one job per
// plugin) the next Capture must not be refused for ~15 s.
func TestPluginIdentify_NewCaptureReplacesEarlierJob_3873(t *testing.T) {
	h := newIdentifyHarness(t)
	pluginJobCaps = func() (int, int) { return 1, 1 }
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		deadline := time.Now().Add(2 * time.Second)
		for pluginJobs.running(viewPluginID) != 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	})
	h.jmu.Lock()
	h.handler = func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, nil
	}
	h.jmu.Unlock()
	first := h.start()
	second := h.start()
	if first == second {
		t.Fatal("same job id twice")
	}
	assertNotice(t, h.do(http.MethodGet, identifyRoute+"?_job="+first, nil, true), "plugin.job.gone")
	deadline := time.Now().Add(2 * time.Second)
	for len(stagedUploads(t)) != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(stagedUploads(t)); n != 1 {
		t.Fatalf("staged uploads = %d, want 1 (the replaced job releases its photo)", n)
	}
}

// A job the plugin runs elsewhere (its own page) is never cancelled to make
// room: that is still busy.
func TestPluginIdentify_Busy429_3873(t *testing.T) {
	h := newIdentifyHarness(t)
	pluginJobCaps = func() (int, int) { return 1, 1 }
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		// Let the job end before the database closes.
		deadline := time.Now().Add(2 * time.Second)
		for pluginJobs.running(viewPluginID) != 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	})
	h.jmu.Lock()
	h.handler = func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, nil
	}
	h.jmu.Unlock()
	pageJob, err := pluginJobs.reserve(viewPluginID, "/plugin/views", func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pluginJobs.finish(pageJob, nil, "", errors.New("test over")) })
	rec := h.post(jpegBytes(1024))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second identify = %d, want 429:\n%s", rec.Code, rec.Body.String())
	}
	assertNotice(t, rec, "plugin.job.busy")
	// The busy refusal releases its own photo.
	time.Sleep(20 * time.Millisecond)
	if n := len(stagedUploads(t)); n != 0 {
		t.Fatalf("staged uploads = %d, want 0 (the busy refusal releases its own)", n)
	}
}

func TestPluginIdentify_PollGoneAndForeign_3873(t *testing.T) {
	h := newIdentifyHarness(t)
	assertNotice(t, h.do(http.MethodGet, identifyRoute+"?_job=0123456789abcdef0123456789abcdef", nil, true), "plugin.job.gone")
	assertNotice(t, h.do(http.MethodGet, identifyRoute, nil, true), "plugin.job.gone")
	// A plugin page's job is not this seam's.
	id, err := pluginJobs.reserve(viewPluginID, "/plugin/views", func() {})
	if err != nil {
		t.Fatal(err)
	}
	assertNotice(t, h.do(http.MethodGet, identifyRoute+"?_job="+id, nil, true), "plugin.job.gone")
}

func TestIdentifyPluginID_3873(t *testing.T) {
	h := newIdentifyHarness(t)
	if got := identifyPluginID(t.Context(), h.d); got != viewPluginID {
		t.Fatalf("resolver = %q, want %q", got, viewPluginID)
	}
	// A lexically earlier subscriber without events:receive is skipped.
	seedTestPlugin(t, h.d.Db, "com.aaa.noperm", "No perm", "1.0.0")
	addIdentifyHook(t, h, "com.aaa.noperm")
	bus := plugins.SharedBus(h.d.Db)
	noop := func(context.Context, plugins.Event) (json.RawMessage, error) { return nil, nil }
	if _, err := bus.SubscribeWithHandler(t.Context(), "com.aaa.noperm", []string{identifyEvent}, noop); err != nil {
		t.Fatal(err)
	}
	if got := identifyPluginID(t.Context(), h.d); got != viewPluginID {
		t.Fatalf("resolver = %q, want %q (the earlier one lacks events:receive)", got, viewPluginID)
	}
	// The resolver runs on every sell-page render: skipping a plugin
	// without events:receive must not write a permission_denied audit row
	// each time (review finding, ut-docs#3873).
	var denials int
	if err := h.d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='permission_denied'`).Scan(&denials); err != nil {
		t.Fatal(err)
	}
	if denials != 0 {
		t.Fatalf("resolver wrote %d permission_denied audit rows, want 0", denials)
	}
	// With it, the lexically first wins.
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('aaa-er','com.aaa.noperm','events:receive',1)`); err != nil {
		t.Fatal(err)
	}
	if got := identifyPluginID(t.Context(), h.d); got != "com.aaa.noperm" {
		t.Fatalf("resolver = %q, want the lexically first com.aaa.noperm", got)
	}
	bus.ResetSubscribers()
	if got := identifyPluginID(t.Context(), h.d); got != "" {
		t.Fatalf("no subscriber: resolver = %q, want \"\"", got)
	}
}

// One camera-identify button: the plugin's when a plugin answers
// catalog.identify, else the built-in AI one.
func TestIndex_PluginIdentifyButton_3873(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	i18n, err := config.NewI18n("web/locales", "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state, Menu: []common.MenuItem{}, Settings: settings.NewStore(db),
		AI: ai.New(ai.Config{Provider: "ollama", Endpoint: "http://127.0.0.1:1"})}
	if !dp.AI.Enabled() {
		t.Fatal("test AI service is not enabled")
	}
	mux := http.NewServeMux()
	registerIndex(mux, dp)
	get := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /: %d", rec.Code)
		}
		return rec.Body.String()
	}
	orig := identifyPluginID
	t.Cleanup(func() { identifyPluginID = orig })

	identifyPluginID = func(context.Context, *common.Deps) string { return "" }
	body := get()
	if !strings.Contains(body, `id="ai-identify-open"`) || !strings.Contains(body, `id="ai-identify-overlay"`) {
		t.Fatal("no plugin: the built-in AI identify must render")
	}
	if strings.Contains(body, `id="plugin-identify-open"`) || strings.Contains(body, `id="plugin-identify-overlay"`) {
		t.Fatal("no plugin answers catalog.identify, but its button rendered")
	}

	identifyPluginID = func(context.Context, *common.Deps) string { return "com.test.identify" }
	body = get()
	if !strings.Contains(body, `id="plugin-identify-open"`) || !strings.Contains(body, `id="plugin-identify-overlay"`) {
		t.Fatal("a plugin answers catalog.identify, but its button/overlay did not render")
	}
	if strings.Contains(body, `id="ai-identify-open"`) || strings.Contains(body, `id="ai-identify-overlay"`) {
		t.Fatal("two camera-identify buttons: the built-in one must step aside for the plugin's")
	}
}
