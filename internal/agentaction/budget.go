package agentaction

import (
	"sync"
	"time"
)

// Tool-call ceiling (ADR-0153 Decision 6, ut-docs#4143): per operator
// request (one chat turn, counted across every event and job of that turn)
// an ai.agent plugin makes at most MaxToolCallsPerRequest tool calls
// (view_query today; ai.tool.invoke.* counts here too once it exists).
// The plugin cannot raise it; the per-event view_query cap still applies
// and the lower bound wins.
const (
	MaxToolCallsPerRequest = 25
	// turnWindow outlives any one turn: a ui ask plus the job it starts
	// (at most 300 s, ADR-0121 §2). A turn's count is forgotten after it.
	turnWindow      = 6 * time.Minute
	maxTrackedTurns = 1024
)

// ToolCallBudget counts tool calls per (session, request). In memory: a
// restart ends every turn anyway.
type ToolCallBudget struct {
	mu    sync.Mutex
	turns map[string]*turnCount
	now   func() time.Time
}

type turnCount struct {
	n     int
	first time.Time
}

// NewToolCallBudget returns an empty budget.
func NewToolCallBudget() *ToolCallBudget {
	return &ToolCallBudget{turns: map[string]*turnCount{}, now: time.Now}
}

// ToolCalls is the till's one budget.
var ToolCalls = NewToolCallBudget()

// Take counts one tool call for op's request and reports whether it is
// within the ceiling. An operator without a session and request is
// refused. When maxTrackedTurns are tracked, a new turn evicts the oldest
// one rather than being refused, so one busy session can never lock every
// other session out (#4143 review); the evicted turn is the oldest, so it
// is almost certainly over.
func (b *ToolCallBudget) Take(op Operator) bool {
	if op.Session == "" || op.Request == "" {
		return false
	}
	key := op.Session + "\x00" + op.Request
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	t, ok := b.turns[key]
	if ok && now.Sub(t.first) >= turnWindow {
		delete(b.turns, key)
		ok = false
	}
	if !ok {
		if len(b.turns) >= maxTrackedTurns {
			b.evictLocked(now)
		}
		t = &turnCount{first: now}
		b.turns[key] = t
	}
	if t.n >= MaxToolCallsPerRequest {
		return false
	}
	t.n++
	return true
}

// evictLocked drops every turn past turnWindow and, if the tracker is
// still full, the oldest one.
func (b *ToolCallBudget) evictLocked(now time.Time) {
	oldest, oldestAt := "", now
	for k, o := range b.turns {
		if now.Sub(o.first) >= turnWindow {
			delete(b.turns, k)
			continue
		}
		if !o.first.After(oldestAt) {
			oldest, oldestAt = k, o.first
		}
	}
	if len(b.turns) >= maxTrackedTurns {
		delete(b.turns, oldest)
	}
}
