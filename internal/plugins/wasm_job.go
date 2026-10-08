package plugins

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/tetratelabs/wazero/api"
)

// Plugin jobs (ADR-0121 §3/§8, ut-docs#3908). A UI action that may run
// longer than an event's deadline answers {"job": {...}}; the page layer
// then asks the plugin's own job event off the request path, with the
// context marked by WithJob. HandleEvent gives such a call:
//   - the job's deadline (limits.long_call_s, at most maxJobDeadline)
//     instead of the event deadline;
//   - never the reserved sale-path call slot, whatever the event name —
//     a 3-minute job cannot starve a payment or a fiscal signature;
//   - the ui.* answer cap (maxUIAnswerBytes): the answer is a view
//     document or a redirect;
//   - a job_progress sink, so the guest can report progress the poll
//     component shows.
//
// Jobs still go through wasmCallGate as ordinary calls; the page layer
// caps them at 2 running per plugin.

// maxJobDeadline is the platform ceiling on a long call (ADR-0121 §2: 300 s
// on every platform). WithJob callers pass EffectiveLimits().LongCallS,
// already clamped; this is the runtime's own bound on top.
const maxJobDeadline = 300 * time.Second

// maxJobProgressKeyLen caps job_progress's message key, checked before it
// is read from guest memory.
const maxJobProgressKeyLen = 256

// JobCall marks a call as a job.
type JobCall struct {
	// Deadline replaces the event deadline (clamped to maxJobDeadline;
	// <= 0 means maxJobDeadline).
	Deadline time.Duration
	// Progress receives job_progress reports: pct already clamped to
	// 0..100, key "" or a key the sink must check is in the plugin's own
	// locale bundle — a non-nil error refuses the report (hostErrInvalid).
	// nil: job_progress answers hostErrNotFound.
	Progress func(pct int, key string) error
}

type jobCallKey struct{}

// WithJob returns ctx marking the call made with it as job j.
func WithJob(ctx context.Context, j JobCall) context.Context {
	return context.WithValue(ctx, jobCallKey{}, j)
}

// JobFrom returns the job ctx carries, if any — for an in-process handler
// that reports progress through j.Progress itself.
func JobFrom(ctx context.Context) (JobCall, bool) {
	j, ok := ctx.Value(jobCallKey{}).(JobCall)
	return j, ok
}

// deadline is j's effective deadline.
func (j JobCall) deadline() time.Duration {
	if j.Deadline <= 0 || j.Deadline > maxJobDeadline {
		return maxJobDeadline
	}
	return j.Deadline
}

// hostJobProgress is job_progress(pct, key_ptr, key_len) -> i32 (ABI 3):
// reports the running job's progress. pct is clamped to 0..100; the key is
// empty (keep the core "working" message) or a key from the plugin's own
// locale bundle. Returns 0, hostErrNotFound outside a job, hostErrInvalid
// for an oversize, unreadable, non-UTF-8 or foreign key. No permission:
// a plugin only ever reports on its own job.
func hostJobProgress(ctx context.Context, m api.Module, pct, keyPtr, keyLen uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	if s.jobProgress == nil {
		return hostErrNotFound
	}
	if keyLen > maxJobProgressKeyLen {
		return hostErrInvalid
	}
	raw, ok := readGuest(m, keyPtr, keyLen)
	if !ok || !utf8.Valid(raw) {
		return hostErrInvalid
	}
	if pct > 100 {
		pct = 100
	}
	if err := s.jobProgress(int(pct), string(raw)); err != nil {
		return hostErrInvalid
	}
	return 0
}
