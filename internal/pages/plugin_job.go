package pages

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pluginview"
)

// Plugin jobs (ADR-0121 §8, ut-docs#3908; format: ut-docs
// reference/plugin-views.md). A ui.action.ask answer {"job": {"event":
// "<plugin-id>.<name>"}} makes the action answer at once with core's poll
// component while the plugin's own event runs off the request path, with
// deadline limits.long_call_s and never on the sale path
// (plugins.WithJob). The poll asks GET <entry route>?_job=<id> about every
// second: running → the poll again (progress from job_progress), done →
// the result (a document, or a redirect), failed → the unavailable notice,
// unknown/expired/another route's → the plugin.job.gone notice.
//
// The registry is in memory: a job never outlives the process, and a
// result arriving after the operator left is dropped — an unpolled running
// job is cancelled after pluginJobUnpolledTTL, an unfetched result pruned
// after pluginJobResultTTL.

// Timings; vars so tests can shorten them. Each job reads them once, at
// start.
var (
	// pluginJobUnpolledTTL: a running job nobody polled for this long is
	// cancelled (its slot freed) and forgotten.
	pluginJobUnpolledTTL = 15 * time.Second
	// pluginJobResultTTL: a finished job nobody fetched for this long is
	// dropped.
	pluginJobResultTTL = 30 * time.Second
	// pluginJobWatchEvery: how often a running job checks it is still
	// being polled.
	pluginJobWatchEvery = time.Second
	// pluginJobDeadline is a job's deadline: the plugin's
	// limits.long_call_s (EffectiveLimits, platform-clamped to ≤ 300 s; the
	// default when the manifest cannot be read).
	pluginJobDeadline = func(ctx context.Context, d *common.Deps, pluginID string) time.Duration {
		m, ok, err := plugins.InstalledManifest(ctx, d.Db, pluginID)
		if err != nil {
			logging.L().Warnf("plugin job %s: manifest unreadable, using the default long-call limit: %v", pluginID, err)
		}
		if err != nil || !ok {
			m = &plugins.Manifest{}
		}
		return time.Duration(m.EffectiveLimits(goruntime.GOOS).LongCallS) * time.Second
	}
)

// pluginJobCaps: running jobs per plugin and across all plugins
// (ADR-0121 §8), from the call gate's slots (plugins.JobCaps: 1 and 3 on
// mobile, 2 and 7 on desktop). A job past either is refused with the
// plugin.job.busy notice. A var so tests can set them.
var pluginJobCaps = func() (perPlugin, global int) { return plugins.JobCaps(goruntime.GOOS) }

// pluginJobMaxUnfetched: finished jobs per plugin whose result nobody has
// fetched yet; when another finishes, the oldest is dropped.
const pluginJobMaxUnfetched = 4

// pluginJobParam is the poll's query parameter.
const pluginJobParam = "_job"

var errPluginJobBusy = errors.New("plugin already runs its maximum number of jobs")

type pluginJobState int

const (
	pluginJobRunning pluginJobState = iota
	pluginJobDone
	pluginJobFailed
)

type pluginJob struct {
	id, pluginID, route string
	state               pluginJobState
	pct                 int // -1: nothing reported yet
	key                 string
	doc                 *pluginview.Document
	redirect            string
	lastPoll            time.Time
	finishedAt          time.Time
	// shown, shownPct, shownKey: the progress the last rendered poll
	// showed, so an htmx poll with nothing new answers 204 (no swap, no
	// re-announcement of the poll's live region).
	shown       bool
	shownPct    int
	shownKey    string
	unpolledTTL time.Duration
	resultTTL   time.Duration
	cancel      context.CancelFunc
}

// pluginJobSnapshot is what one poll sees. unchanged: the job is running
// and its progress is what the last rendered poll showed.
type pluginJobSnapshot struct {
	state     pluginJobState
	pct       int
	key       string
	doc       *pluginview.Document
	redirect  string
	unchanged bool
}

// pluginJobRegistry holds the till's jobs, keyed by id.
type pluginJobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*pluginJob
}

func newPluginJobRegistry() *pluginJobRegistry {
	return &pluginJobRegistry{jobs: map[string]*pluginJob{}}
}

// pluginJobs is the process's job registry.
var pluginJobs = newPluginJobRegistry()

// newPluginJobID is 128 random bits, hex: the id is the only thing that
// names a job, so it must not be guessable.
func newPluginJobID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// runningLocked counts pluginID's running jobs and all running jobs.
func (r *pluginJobRegistry) runningLocked(pluginID string) (mine, all int) {
	for _, j := range r.jobs {
		if j.state != pluginJobRunning {
			continue
		}
		all++
		if j.pluginID == pluginID {
			mine++
		}
	}
	return mine, all
}

// reserve registers a running job for pluginID on route, or refuses with
// errPluginJobBusy when the plugin, or the till, already runs its cap
// (pluginJobCaps).
func (r *pluginJobRegistry) reserve(pluginID, route string, cancel context.CancelFunc) (string, error) {
	id, err := newPluginJobID()
	if err != nil {
		return "", err
	}
	perCap, globalCap := pluginJobCaps()
	r.mu.Lock()
	defer r.mu.Unlock()
	if mine, all := r.runningLocked(pluginID); mine >= perCap || all >= globalCap {
		return "", errPluginJobBusy
	}
	r.jobs[id] = &pluginJob{
		id: id, pluginID: pluginID, route: route,
		state: pluginJobRunning, pct: -1,
		// The action's answer shows the poll with nothing reported yet.
		shown: true, shownPct: -1,
		lastPoll:    time.Now(),
		unpolledTTL: pluginJobUnpolledTTL,
		resultTTL:   pluginJobResultTTL,
		cancel:      cancel,
	}
	return id, nil
}

// progress records a job_progress report for a running job.
func (r *pluginJobRegistry) progress(id string, pct int, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok && j.state == pluginJobRunning {
		j.pct = min(max(pct, 0), 100)
		if key != "" {
			j.key = key
		}
	}
}

// finish records a running job's outcome (err != nil: failed) and
// schedules the unfetched result's pruning. A job already forgotten
// (abandoned) stays forgotten: its result is dropped.
func (r *pluginJobRegistry) finish(id string, doc *pluginview.Document, redirect string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok || j.state != pluginJobRunning {
		return
	}
	if err != nil {
		j.state = pluginJobFailed
	} else {
		j.state, j.doc, j.redirect = pluginJobDone, doc, redirect
	}
	j.finishedAt = time.Now()
	j.cancel()
	time.AfterFunc(j.resultTTL, func() { r.drop(id) })
	r.capUnfetchedLocked(j.pluginID)
}

// capUnfetchedLocked drops pluginID's oldest finished jobs while it has
// more than pluginJobMaxUnfetched results nobody fetched.
func (r *pluginJobRegistry) capUnfetchedLocked(pluginID string) {
	for {
		var oldest *pluginJob
		n := 0
		for _, j := range r.jobs {
			if j.pluginID != pluginID || j.state == pluginJobRunning {
				continue
			}
			n++
			if oldest == nil || j.finishedAt.Before(oldest.finishedAt) {
				oldest = j
			}
		}
		if n <= pluginJobMaxUnfetched {
			return
		}
		delete(r.jobs, oldest.id)
	}
}

// drop forgets a finished job (its result was never fetched).
func (r *pluginJobRegistry) drop(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok && j.state != pluginJobRunning {
		delete(r.jobs, id)
	}
}

// abandon cancels and forgets a running job nobody polled for its
// unpolled TTL, reporting whether it did.
func (r *pluginJobRegistry) abandon(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return true // already gone
	}
	if j.state != pluginJobRunning || time.Since(j.lastPoll) <= j.unpolledTTL {
		return false
	}
	j.cancel()
	delete(r.jobs, id)
	return true
}

// poll returns the job's state for pluginID's entry route: false when the
// id is unknown, expired, or belongs to another plugin or route. Every
// poll refreshes the job's last-polled time. A finished job is handed out
// once, then forgotten — unless !consume (a HEAD request, which carries
// no body to hand it out in, nor shows any progress). On a consuming poll
// of a running job, the snapshot says whether its progress is unchanged
// since the last one, and records it as shown.
func (r *pluginJobRegistry) poll(id, pluginID, route string, consume bool) (pluginJobSnapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok || j.pluginID != pluginID || j.route != route {
		return pluginJobSnapshot{}, false
	}
	j.lastPoll = time.Now()
	snap := pluginJobSnapshot{state: j.state, pct: j.pct, key: j.key, doc: j.doc, redirect: j.redirect}
	switch {
	case !consume:
	case j.state != pluginJobRunning:
		delete(r.jobs, id)
	default:
		snap.unchanged = j.shown && j.shownPct == j.pct && j.shownKey == j.key
		j.shown, j.shownPct, j.shownKey = true, j.pct, j.key
	}
	return snap, true
}

// startPluginJob runs event for entry's plugin as a job: payload is the
// action's payload, sent with the job's id as job_id; vctx is what the
// result is validated against (and job_progress keys too). It returns the
// job id at once, or errPluginJobBusy.
func startPluginJob(ctx context.Context, d *common.Deps, entry data.PageEntryRow, event string, payload map[string]any, vctx pluginview.Context) (string, error) {
	reg := pluginJobs
	watchEvery := pluginJobWatchEvery
	deadline := pluginJobDeadline(ctx, d, entry.PluginID)
	// The job outlives the request (WithoutCancel) but never its own
	// deadline.
	jctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deadline)
	id, err := reg.reserve(entry.PluginID, entry.Route, cancel)
	if err != nil {
		cancel()
		return "", err
	}
	job := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		job[k] = v
	}
	job["job_id"] = id
	ownKeys := vctx.OwnKeys
	jctx = plugins.WithJob(jctx, plugins.JobCall{
		Deadline: deadline,
		Progress: func(pct int, key string) error {
			if key != "" && !ownKeys[key] {
				return fmt.Errorf("progress key %q is not in this plugin's own locale bundle", key)
			}
			reg.progress(id, pct, key)
			return nil
		},
	})

	go func() {
		defer logging.RecoverAndLog("pages.pluginJob")
		defer cancel()
		type result struct {
			raw []byte
			ok  bool
			err error
		}
		done := make(chan result, 1)
		go func() {
			defer logging.RecoverAndLog("pages.pluginJobAsk")
			// Sent by a deferred call so a panicking handler still ends
			// the job at once (it runs before RecoverAndLog recovers).
			res := result{err: errors.New("plugin job panicked")}
			defer func() { done <- res }()
			raw, ok, err := plugins.SharedBus(d.Db).AskPlugin(jctx, entry.PluginID, event, job)
			res = result{raw, ok, err}
		}()
		tick := time.NewTicker(watchEvery)
		defer tick.Stop()
		for {
			select {
			case res := <-done:
				err := res.err
				var doc *pluginview.Document
				var redirect string
				switch {
				case err != nil:
				case !res.ok:
					err = errPluginNoAnswer
				default:
					doc, redirect, err = pluginview.DecodeJobResult(res.raw, vctx)
				}
				if err != nil {
					logging.L().Warnf("plugin job %s %s (%s): %v", entry.PluginID, event, entry.Route, err)
				}
				reg.finish(id, doc, redirect, err)
				return
			case <-jctx.Done():
				// The deadline, even if the handler ignores its context.
				// (An abandoned job returned below before cancelling.)
				logging.L().Warnf("plugin job %s %s (%s): %v", entry.PluginID, event, entry.Route, jctx.Err())
				reg.finish(id, nil, "", jctx.Err())
				return
			case <-tick.C:
				if reg.abandon(id) {
					logging.L().Infof("plugin job %s %s (%s): nobody polled it; cancelled", entry.PluginID, event, entry.Route)
					return
				}
			}
		}
	}()
	return id, nil
}
