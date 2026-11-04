package pages

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Data-management background job (ut-docs#5445, #5335 review R-1).
//
// The four Settings -> Data actions (reset-transactions, archive restore,
// archive purge, catalog cleanup) each run one all-or-nothing SQLite
// transaction. They used to run it on the request's context, so the
// client's 30 s prompt timeout closed the connection, net/http cancelled
// r.Context() and database/sql rolled the transaction back: past ~30 s the
// action could never succeed from the UI. Now the handler gates and
// pre-checks synchronously, then starts the transaction here on a context
// that keeps the request's values but not its cancellation
// (context.WithoutCancel) with a generous ceiling, and answers 202 with a
// job id the Settings page polls (GET /api/data/jobs/{id}).
//
// One till-wide slot: a data action running blocks every other data action
// (they all lock the same tables), whichever session starts it. The
// registry is in memory only -- after a restart the transaction is either
// fully committed or rolled back by SQLite, and a poll for the old id gets
// 404 ("interrupted"). It deliberately is not pluginJobRegistry: that one
// cancels a job nobody polls, which is exactly what this must not do.
// ADR-0087 step-up is unchanged (it runs before the start) and ADR-0042
// atomicity is kept (one tx per job).

// Timings; vars so tests can shorten them.
var (
	// dataJobTimeout is the ceiling for one job; hitting it rolls the tx
	// back and the job reports failed.
	dataJobTimeout = 30 * time.Minute
	// dataJobResultTTL keeps a finished result for a late poll or reload.
	dataJobResultTTL = 15 * time.Minute
)

var errDataJobBusy = errors.New("a data-management action is already running")

type dataJobState string

const (
	dataJobRunning dataJobState = "running"
	dataJobDone    dataJobState = "done"
	dataJobFailed  dataJobState = "failed"
)

// Action ids reported to the client.
const (
	dataActionReset   = "reset_transactions"
	dataActionRestore = "archive_restore"
	dataActionPurge   = "archive_purge"
	dataActionCleanup = "catalog_cleanup"
)

type dataJob struct {
	id         string
	action     string
	state      dataJobState
	httpStatus int
	body       map[string]any
	finishedAt time.Time
	fetched    bool // a poll has delivered the finished result
}

type dataJobSlot struct {
	mu  sync.Mutex
	cur *dataJob
	wg  sync.WaitGroup // tests wait on it
}

var dataJobs = &dataJobSlot{}

// pruneLocked drops a finished result past its TTL.
func (s *dataJobSlot) pruneLocked() {
	if s.cur != nil && s.cur.state != dataJobRunning && time.Since(s.cur.finishedAt) > dataJobResultTTL {
		s.cur = nil
	}
}

// running reports whether a job holds the slot (fast pre-PIN busy check).
func (s *dataJobSlot) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil && s.cur.state == dataJobRunning
}

// get returns a copy of the job with this id, if it is still known.
func (s *dataJobSlot) get(id string) (dataJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if s.cur == nil || s.cur.id != id {
		return dataJob{}, false
	}
	if s.cur.state != dataJobRunning {
		s.cur.fetched = true
	}
	return *s.cur, true
}

// current returns the running job, or a finished one nobody has fetched yet,
// for the Settings render (a reload mid-job resumes; a delivered result is
// not replayed on every later reload).
func (s *dataJobSlot) current() (dataJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if s.cur == nil || (s.cur.state != dataJobRunning && s.cur.fetched) {
		return dataJob{}, false
	}
	return *s.cur, true
}

func (s *dataJobSlot) finish(id string, state dataJobState, status int, body map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil && s.cur.id == id && s.cur.state == dataJobRunning {
		s.cur.state, s.cur.httpStatus, s.cur.body, s.cur.finishedAt = state, status, body, time.Now()
	}
}

// start reserves the slot and runs `run` off the request path. run must not
// touch the request or response writer; it returns the HTTP status and JSON
// body the synchronous handler used to write.
func (s *dataJobSlot) start(d *common.Deps, parent context.Context, action string,
	run func(ctx context.Context) (status int, body map[string]any)) (string, error) {
	id, err := newPluginJobID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.cur != nil && s.cur.state == dataJobRunning {
		s.mu.Unlock()
		return "", errDataJobBusy
	}
	s.cur = &dataJob{id: id, action: action, state: dataJobRunning}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), dataJobTimeout)
	s.wg.Add(1)
	d.AsyncWork.Add(1)
	s.mu.Unlock()

	go func() {
		defer logging.RecoverAndLog("pages.dataJob")
		defer d.AsyncWork.Done()
		defer s.wg.Done()
		defer cancel()
		// A panic in run leaves the job running; mark it failed so the slot
		// frees. finish is a no-op once the job already has a result.
		defer s.finish(id, dataJobFailed, http.StatusInternalServerError, nil)
		status, body := run(ctx)
		if ctx.Err() != nil && status >= http.StatusInternalServerError {
			s.finish(id, dataJobFailed, status, body)
			return
		}
		s.finish(id, dataJobDone, status, body)
	}()
	return id, nil
}
