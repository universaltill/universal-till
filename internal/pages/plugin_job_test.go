package pages

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Plugin jobs (ADR-0121 §8, ut-docs#3908): a ui.action.ask answer
// {"job":{"event":...}} returns a core poll at once and runs the plugin's
// own event off the request path; GET <route>?_job=<id> polls it.

const viewJobEvent = viewPluginID + ".identify"

var jobIDRe = regexp.MustCompile(`_job=([0-9a-f]{32})`)

// jobHarness is viewHarness plus a subscription for the plugin's own job
// event, and short registry timings.
type jobHarness struct {
	*viewHarness
	jobMu      sync.Mutex
	jobHandler func(ctx context.Context, ev plugins.Event) (json.RawMessage, error)
	jobPayload map[string]any
}

func newJobHarness(t *testing.T) *jobHarness {
	t.Helper()
	h := &jobHarness{viewHarness: newViewHarness(t)}
	origReg, origUnpolled, origResult, origWatch, origDeadline, origCaps := pluginJobs, pluginJobUnpolledTTL, pluginJobResultTTL, pluginJobWatchEvery, pluginJobDeadline, pluginJobCaps
	pluginJobs = newPluginJobRegistry()
	pluginJobWatchEvery = 10 * time.Millisecond
	// Desktop caps whatever the test host is; tests that need others set them.
	pluginJobCaps = func() (int, int) { return plugins.JobCaps("linux") }
	t.Cleanup(func() {
		pluginJobs, pluginJobUnpolledTTL, pluginJobResultTTL, pluginJobWatchEvery, pluginJobDeadline, pluginJobCaps = origReg, origUnpolled, origResult, origWatch, origDeadline, origCaps
	})
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_hooks(id,plugin_id,event,action,is_active) VALUES(?,?,?,'job',1)`, viewPluginID+"-hjob", viewPluginID, viewJobEvent); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.SharedBus(h.d.Db).SubscribeWithHandler(t.Context(), viewPluginID, []string{viewJobEvent},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			h.jobMu.Lock()
			h.jobPayload = nil
			_ = json.Unmarshal(ev.Payload, &h.jobPayload)
			handler := h.jobHandler
			h.jobMu.Unlock()
			if handler == nil {
				return nil, nil
			}
			return handler(ctx, ev)
		}); err != nil {
		t.Fatalf("subscribe job event: %v", err)
	}
	h.answerWith(`{"job":{"event":"` + viewJobEvent + `"}}`)
	return h
}

func (h *jobHarness) onJob(f func(ctx context.Context, ev plugins.Event) (json.RawMessage, error)) {
	h.jobMu.Lock()
	h.jobHandler = f
	h.jobMu.Unlock()
}

// startJob posts an action answered with a job and returns the poll's id.
func (h *jobHarness) startJob(htmx bool) (string, *httptest.ResponseRecorder) {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"identify"}, "note": {"tea"}}, htmx)
	m := jobIDRe.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil {
		h.t.Fatalf("job action = %d, no poll in:\n%s", rec.Code, rec.Body.String())
	}
	return m[1], rec
}

func (h *jobHarness) poll(route, id string) *httptest.ResponseRecorder {
	return h.do(http.MethodGet, route+"?_job="+id, nil, true)
}

// stillRunning: an htmx poll answer for a running job — the poll again,
// or 204 when nothing changed since the last one.
func stillRunning(rec *httptest.ResponseRecorder) bool {
	return rec.Code == http.StatusNoContent || strings.Contains(rec.Body.String(), `class="plugin-view-poll"`)
}

// pollUntil polls until the job is no longer running.
func (h *jobHarness) pollUntil(id string) *httptest.ResponseRecorder {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec := h.poll("/plugin/views", id)
		if !stillRunning(rec) {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatal("job never finished")
	return nil
}

func assertNotice(t *testing.T, rec *httptest.ResponseRecorder, key string) {
	t.Helper()
	msg := httpx.T("en", key)
	if msg == key {
		t.Fatalf("%s is not in en.json", key)
	}
	if !strings.Contains(rec.Body.String(), template.HTMLEscapeString(msg)) {
		t.Fatalf("no %s notice in:\n%s", key, rec.Body.String())
	}
}

func TestPluginJob_PollThenResult_3908(t *testing.T) {
	h := newJobHarness(t)
	// The view ask bound is 100 ms; the job runs 300 ms past it.
	orig := pluginViewTimeout
	pluginViewTimeout = 100 * time.Millisecond
	t.Cleanup(func() { pluginViewTimeout = orig })

	progressed := make(chan struct{})
	release := make(chan struct{})
	var foreignErr, ownErr atomic.Value
	h.onJob(func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		job, ok := plugins.JobFrom(ctx)
		if !ok || job.Progress == nil {
			return nil, errors.New("job event run without a job context")
		}
		foreignErr.Store(job.Progress(10, "nav.home") != nil)
		ownErr.Store(job.Progress(40, "plugin.views.hello") == nil)
		close(progressed)
		<-release
		time.Sleep(300 * time.Millisecond)
		return json.RawMessage(`{"document":{"version":1,"title":{"literal":"Found"},"components":[{"type":"text","text":{"literal":"Identified <tea>"}}]}}`), nil
	})

	start := time.Now()
	id, rec := h.startJob(true)
	if time.Since(start) > time.Second {
		t.Fatal("the job action waited for the job")
	}
	body := rec.Body.String()
	for _, want := range []string{
		`class="plugin-view-poll" role="status" aria-live="polite"`,
		`hx-get="/plugin/views?_job=` + id + `"`,
		`hx-trigger="every 1s"`,
		`hx-target="#plugin-view"`,
		`<a class="plugin-view-poll-refresh" href="/plugin/views?_job=` + id + `">`,
		`<progress`,
		template.HTMLEscapeString(httpx.T("en", "plugin.job.running")),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("poll missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<html") {
		t.Fatal("htmx job action must answer the view fragment only")
	}

	<-progressed
	if foreignErr.Load() != true {
		t.Error("a progress key outside the plugin's own bundle was accepted")
	}
	if ownErr.Load() != true {
		t.Error("a progress key from the plugin's own bundle was refused")
	}
	// The job event got the action payload plus job_id.
	h.jobMu.Lock()
	pay := h.jobPayload
	h.jobMu.Unlock()
	if pay["job_id"] != id || pay["action"] != "identify" || pay["view"] != "views.home" {
		t.Errorf("job payload = %v", pay)
	}
	if f, _ := pay["form"].(map[string]any); f["note"] != "tea" {
		t.Errorf("job payload form = %v", pay["form"])
	}

	rec = h.poll("/plugin/views", id)
	body = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `value="40"`) || !strings.Contains(body, "plugin.views.hello") {
		t.Fatalf("running poll = %d, want progress 40 and the plugin's message:\n%s", rec.Code, body)
	}
	if strings.Contains(body, "<html") {
		t.Fatal("htmx poll must answer the view fragment only")
	}

	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for pluginJobs.running(viewPluginID) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// A HEAD has no body to hand the result out in: it must not use it up.
	if rec := h.do(http.MethodHead, "/plugin/views?_job="+id, nil, true); rec.Code != http.StatusOK {
		t.Fatalf("HEAD poll = %d", rec.Code)
	}
	rec = h.pollUntil(id)
	body = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Identified &lt;tea&gt;") {
		t.Fatalf("finished poll = %d, want the result document:\n%s", rec.Code, body)
	}
	if !strings.Contains(body, `id="plugin-view-title" hx-swap-oob="true"`) {
		t.Error("the result's title must reach the heading out of band")
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("result arrived before the job could have run past the view deadline")
	}
	// Fetched once: the job is gone.
	assertNotice(t, h.poll("/plugin/views", id), "plugin.job.gone")
}

func TestPluginJob_NoJSPollIsAFullPage_3908(t *testing.T) {
	h := newJobHarness(t)
	release := make(chan struct{})
	h.onJob(func(context.Context, plugins.Event) (json.RawMessage, error) {
		<-release
		return json.RawMessage(`{"redirect":"/plugin/views/next"}`), nil
	})
	id, rec := h.startJob(false)
	if !strings.Contains(rec.Body.String(), "<html") || !strings.Contains(rec.Body.String(), `href="/plugin/views?_job=`+id+`"`) {
		t.Fatalf("no-JS job action must render the page with a refresh link:\n%s", rec.Body.String())
	}
	assertPluginPolicy(t, rec)
	rec = h.do(http.MethodGet, "/plugin/views?_job="+id, nil, false)
	if !strings.Contains(rec.Body.String(), "<html") || !strings.Contains(rec.Body.String(), `class="plugin-view-poll"`) {
		t.Fatalf("no-JS poll must render the page:\n%s", rec.Body.String())
	}
	// A redirect result redirects, like an action.
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec = h.do(http.MethodGet, "/plugin/views?_job="+id, nil, false)
		if rec.Code == http.StatusSeeOther {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/plugin/views/next" {
		t.Fatalf("redirect result = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestPluginJob_RedirectResultHTMX_3908(t *testing.T) {
	h := newJobHarness(t)
	h.onJob(func(context.Context, plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"redirect":"/plugin/views/next"}`), nil
	})
	id, _ := h.startJob(true)
	deadline := time.Now().Add(5 * time.Second)
	var rec *httptest.ResponseRecorder
	for time.Now().Before(deadline) {
		rec = h.poll("/plugin/views", id)
		if rec.Header().Get("HX-Redirect") != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec.Header().Get("HX-Redirect") != "/plugin/views/next" {
		t.Fatalf("htmx redirect result = %d %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
}

func TestPluginJob_ThirdJobBusy_3908(t *testing.T) {
	h := newJobHarness(t)
	release := make(chan struct{})
	h.onJob(func(ctx context.Context, _ plugins.Event) (json.RawMessage, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return json.RawMessage(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"ok"}}]}}`), nil
	})
	first, _ := h.startJob(true)
	second, _ := h.startJob(true)
	if first == second {
		t.Fatal("two jobs share an id")
	}
	rec := h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"identify"}}, true)
	if jobIDRe.MatchString(rec.Body.String()) {
		t.Fatal("a third concurrent job for one plugin was started")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("busy status = %d, want 429", rec.Code)
	}
	assertNotice(t, rec, "plugin.job.busy")

	// Another plugin is not limited by this one's jobs: its cap is its own.
	if n := pluginJobs.running("com.test.other"); n != 0 {
		t.Fatalf("other plugin running = %d", n)
	}
	// A finished job frees its slot.
	close(release)
	h.pollUntil(first)
	h.pollUntil(second)
	if id, _ := h.startJob(true); id == "" {
		t.Fatal("no job after the slots freed")
	}
}

func TestPluginJob_OnlyOwnRoute_3908(t *testing.T) {
	h := newJobHarness(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h.onJob(func(ctx context.Context, _ plugins.Event) (json.RawMessage, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, ctx.Err()
	})
	id, _ := h.startJob(true)
	// Another plugin's route never sees it — nor does another route of the
	// same plugin.
	for _, route := range []string{"/plugin/other", "/plugin/views/next"} {
		rec := h.poll(route, id)
		assertNotice(t, rec, "plugin.job.gone")
		if strings.Contains(rec.Body.String(), id) {
			t.Errorf("%s leaked the job id", route)
		}
	}
	// Unknown and malformed ids are gone too.
	for _, bad := range []string{strings.Repeat("0", 32), "", "../x"} {
		assertNotice(t, h.poll("/plugin/views", url.QueryEscape(bad)), "plugin.job.gone")
	}
	// The job itself is still there on its own route.
	if rec := h.poll("/plugin/views", id); !stillRunning(rec) {
		t.Fatalf("own route lost the job:\n%s", rec.Body.String())
	}
}

func TestPluginJob_FailedAndTimedOut_3908(t *testing.T) {
	h := newJobHarness(t)
	t.Run("plugin error", func(t *testing.T) {
		h.onJob(func(context.Context, plugins.Event) (json.RawMessage, error) { return nil, errors.New("boom") })
		id, _ := h.startJob(true)
		assertUnavailable(t, h.pollUntil(id), http.StatusOK)
	})
	t.Run("invalid result", func(t *testing.T) {
		h.onJob(func(context.Context, plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"job":{"event":"` + viewJobEvent + `"}}`), nil
		})
		id, _ := h.startJob(true)
		assertUnavailable(t, h.pollUntil(id), http.StatusOK)
	})
	t.Run("deadline", func(t *testing.T) {
		pluginJobDeadline = func(context.Context, *common.Deps, string) time.Duration { return 50 * time.Millisecond }
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		// Ignores its context on purpose: the job still ends at its deadline.
		h.onJob(func(context.Context, plugins.Event) (json.RawMessage, error) {
			<-release
			return nil, nil
		})
		id, _ := h.startJob(true)
		assertUnavailable(t, h.pollUntil(id), http.StatusOK)
		if n := pluginJobs.running(viewPluginID); n != 0 {
			t.Fatalf("a timed-out job still holds a slot (%d running)", n)
		}
	})
	t.Run("panic", func(t *testing.T) {
		h.onJob(func(context.Context, plugins.Event) (json.RawMessage, error) { panic("boom") })
		id, _ := h.startJob(true)
		assertUnavailable(t, h.pollUntil(id), http.StatusOK)
	})
}

func TestPluginJob_UnpolledCancelled_3908(t *testing.T) {
	h := newJobHarness(t)
	pluginJobUnpolledTTL = 150 * time.Millisecond
	cancelled := make(chan struct{})
	h.onJob(func(ctx context.Context, _ plugins.Event) (json.RawMessage, error) {
		select {
		case <-ctx.Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
		}
		return nil, ctx.Err()
	})
	id, _ := h.startJob(true)
	// Polled: kept alive past the TTL.
	for i := 0; i < 5; i++ {
		time.Sleep(60 * time.Millisecond)
		if rec := h.poll("/plugin/views", id); !stillRunning(rec) {
			t.Fatalf("a polled job was dropped:\n%s", rec.Body.String())
		}
	}
	// Left alone: cancelled, slot freed, gone.
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("an unpolled job's context was never cancelled")
	}
	deadline := time.Now().Add(2 * time.Second)
	for pluginJobs.running(viewPluginID) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := pluginJobs.running(viewPluginID); n != 0 {
		t.Fatalf("an abandoned job still holds a slot (%d running)", n)
	}
	assertNotice(t, h.poll("/plugin/views", id), "plugin.job.gone")
}

func TestPluginJob_UnfetchedResultDropped_3908(t *testing.T) {
	h := newJobHarness(t)
	pluginJobResultTTL = 50 * time.Millisecond
	done := make(chan struct{})
	h.onJob(func(context.Context, plugins.Event) (json.RawMessage, error) {
		defer close(done)
		return json.RawMessage(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"late"}}]}}`), nil
	})
	id, _ := h.startJob(true)
	<-done
	time.Sleep(300 * time.Millisecond)
	rec := h.poll("/plugin/views", id)
	if strings.Contains(rec.Body.String(), "late") {
		t.Fatal("a result nobody fetched in time was still delivered")
	}
	assertNotice(t, rec, "plugin.job.gone")
}

func TestPluginJob_IDsAreRandom128Bit_3908(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := newPluginJobID()
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) || seen[id] {
			t.Fatalf("job id %q not 128-bit hex or repeated", id)
		}
		seen[id] = true
	}
}

// blockingJob makes the harness's job wait for release (or its context).
func (h *jobHarness) blockingJob(release <-chan struct{}) {
	h.onJob(func(ctx context.Context, _ plugins.Event) (json.RawMessage, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return json.RawMessage(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"ok"}}]}}`), nil
	})
}

// The per-plugin cap follows the call gate (review of ut-docs#3908): on
// mobile the gate has one ordinary slot per plugin, so a second job would
// only queue for it with its deadline ticking and starve the plugin's own
// page asks — it is refused instead.
func TestPluginJob_MobileCapIsOne_3908(t *testing.T) {
	h := newJobHarness(t)
	pluginJobCaps = func() (int, int) { return plugins.JobCaps("android") }
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h.blockingJob(release)
	h.startJob(true)
	rec := h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"identify"}}, true)
	if jobIDRe.MatchString(rec.Body.String()) {
		t.Fatal("a second concurrent job was started on mobile")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("busy status = %d, want 429", rec.Code)
	}
	assertNotice(t, rec, "plugin.job.busy")
}

// The busy notice serves both caps, so it must not name a count.
func TestPluginJob_BusyNoticeIsCountFree_3908(t *testing.T) {
	for _, loc := range []string{"en", "ar", "fa", "tr"} {
		msg := httpx.T(loc, "plugin.job.busy")
		for _, word := range []string{"two", "مهمتين", "دو کار", "iki"} {
			if strings.Contains(msg, word) {
				t.Errorf("%s plugin.job.busy names a count (%q): %s", loc, word, msg)
			}
		}
	}
}

// A global cap across plugins keeps jobs from taking most of the till's
// ordinary WASM slots.
func TestPluginJob_GlobalCap_3908(t *testing.T) {
	reg := newPluginJobRegistry()
	orig := pluginJobCaps
	t.Cleanup(func() { pluginJobCaps = orig })
	pluginJobCaps = func() (int, int) { return 2, 3 }
	noop := func() {}
	for _, p := range []string{"a", "a", "b"} {
		if _, err := reg.reserve(p, "/plugin/"+p, noop); err != nil {
			t.Fatalf("reserve %s: %v", p, err)
		}
	}
	if _, err := reg.reserve("c", "/plugin/c", noop); !errors.Is(err, errPluginJobBusy) {
		t.Fatalf("a job past the global cap: err = %v, want busy", err)
	}
	if _, err := reg.reserve("b", "/plugin/b", noop); !errors.Is(err, errPluginJobBusy) {
		t.Fatalf("a job past the global cap (plugin under its own cap): err = %v, want busy", err)
	}
	// Finished jobs don't count.
	for id := range reg.jobs {
		reg.finish(id, nil, "", nil)
		break
	}
	if _, err := reg.reserve("c", "/plugin/c", noop); err != nil {
		t.Fatalf("a slot freed by a finished job: %v", err)
	}

	// Desktop and mobile values reach the registry by default.
	if per, glob := orig(); per < 1 || glob < 1 {
		t.Fatalf("default caps = (%d, %d)", per, glob)
	}
}

// The poll's role=status element must not be re-announced every second:
// an htmx poll that would render the same progress answers 204 (htmx
// keeps the element and its trigger) and still counts as a poll.
func TestPluginJob_UnchangedPollIs204_3908(t *testing.T) {
	h := newJobHarness(t)
	pluginJobUnpolledTTL = 300 * time.Millisecond
	step := make(chan int)
	stepped := make(chan struct{})
	h.onJob(func(ctx context.Context, _ plugins.Event) (json.RawMessage, error) {
		job, _ := plugins.JobFrom(ctx)
		for {
			select {
			case pct, ok := <-step:
				if !ok {
					return json.RawMessage(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"ok"}}]}}`), nil
				}
				_ = job.Progress(pct, "")
				stepped <- struct{}{}
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	})
	id, _ := h.startJob(true)
	// Nothing reported yet: the POST already showed the indeterminate poll.
	if rec := h.poll("/plugin/views", id); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("unchanged first poll = %d, want 204 with no body:\n%s", rec.Code, rec.Body.String())
	}
	step <- 30
	<-stepped
	rec := h.poll("/plugin/views", id)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `value="30"`) {
		t.Fatalf("changed poll = %d, want 200 with 30:\n%s", rec.Code, rec.Body.String())
	}
	// 204s keep the job alive past its unpolled TTL.
	for i := 0; i < 6; i++ {
		time.Sleep(100 * time.Millisecond)
		if rec := h.poll("/plugin/views", id); rec.Code != http.StatusNoContent {
			t.Fatalf("unchanged poll %d = %d, want 204:\n%s", i, rec.Code, rec.Body.String())
		}
	}
	// HEAD never consumes, and never counts as having shown anything.
	if rec := h.do(http.MethodHead, "/plugin/views?_job="+id, nil, true); rec.Code != http.StatusOK {
		t.Fatalf("HEAD poll = %d", rec.Code)
	}
	// The no-JS page always renders.
	if rec := h.do(http.MethodGet, "/plugin/views?_job="+id, nil, false); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `value="30"`) {
		t.Fatalf("no-JS poll = %d, want the page:\n%s", rec.Code, rec.Body.String())
	}
	step <- 60
	<-stepped
	if rec := h.poll("/plugin/views", id); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `value="60"`) {
		t.Fatalf("progressed poll = %d, want 200 with 60:\n%s", rec.Code, rec.Body.String())
	}
	if rec := h.poll("/plugin/views", id); rec.Code != http.StatusNoContent {
		t.Fatalf("poll after 60 = %d, want 204", rec.Code)
	}
	close(step)
	if rec := h.pollUntil(id); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("result = %d:\n%s", rec.Code, rec.Body.String())
	}
}

// Unfetched finished results are bounded in count, not only in time: a
// fifth unfetched result for a plugin drops the oldest.
func TestPluginJob_UnfetchedResultsCapped_3908(t *testing.T) {
	h := newJobHarness(t)
	var n atomic.Int32
	h.onJob(func(context.Context, plugins.Event) (json.RawMessage, error) {
		i := n.Add(1)
		return json.RawMessage(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"result-` + string(rune('0'+i)) + `"}}]}}`), nil
	})
	var ids []string
	for i := 0; i < pluginJobMaxUnfetched+1; i++ {
		id, _ := h.startJob(true)
		ids = append(ids, id)
		deadline := time.Now().Add(5 * time.Second)
		for pluginJobs.running(viewPluginID) != 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(2 * time.Millisecond) // distinct finish times
	}
	assertNotice(t, h.poll("/plugin/views", ids[0]), "plugin.job.gone")
	for i, id := range ids[1:] {
		want := "result-" + string(rune('0'+i+2))
		if rec := h.poll("/plugin/views", id); !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("job %d lost its result (want %s):\n%s", i+2, want, rec.Body.String())
		}
	}
}

// running counts pluginID's running jobs (test helper; production reads
// the count under its own lock in reserve).
func (r *pluginJobRegistry) running(pluginID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	mine, _ := r.runningLocked(pluginID)
	return mine
}
