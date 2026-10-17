package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/logging"
)

// import_rows_submit (ADR-0161, ut-docs#4429; ADR-0164 §2, ut-docs#4526):
// during an API-source or file-rows "import.requested.ask", the import
// plugin hands catalogue rows back to core in batches. Core buffers them in the run's ImportRowSink and applies
// them (internal/catsync) only after the plugin answers ok. Docs:
// architecture/wasm-runtime.md.
//
// Gates: the sink exists only for a hop-0, non-job "import.requested.ask"
// raised by POST /api/data/import (or the import page) for an api-source
// or file-rows entry, and only for
// the plugin it was made for (the handler resolved entry, entities and
// grants first, like a file import). A batch for an entity the run did not
// resolve is -2. Bounded: ImportRowsPerCall rows per call,
// ImportRowsPerRun rows and ImportBytesPerRun bytes per run; over any cap
// the call is -5 and the whole run is refused — never unbounded memory.

const (
	// ImportRowsPerCall caps one import_rows_submit batch.
	ImportRowsPerCall = 500
	// ImportRowsPerRun caps the rows one run buffers.
	ImportRowsPerRun = 20_000
	// ImportBytesPerRun caps the raw batch bytes one run buffers.
	ImportBytesPerRun = 32 << 20
)

// ErrImportRowsCap is why a run over a cap is refused.
var ErrImportRowsCap = errors.New("import plugin submitted more rows than an import run allows")

// ImportRowSink is one import run's row buffer: an API-source run's
// (ADR-0161) or a file-rows run's (ADR-0164 §2, NewFileImportRowSink).
type ImportRowSink struct {
	pluginID string
	entities map[string]bool
	// file: a file-rows run — the ask runs in the import sandbox.
	file bool

	mu      sync.Mutex
	rows    map[string][]json.RawMessage
	n       int
	bytes   int64
	refused error
}

// NewImportRowSink makes the buffer for pluginID's run over the resolved
// entities (requested ∩ declared ∩ granted "<entity>:write").
func NewImportRowSink(pluginID string, entities []string) *ImportRowSink {
	s := &ImportRowSink{pluginID: pluginID, entities: map[string]bool{}, rows: map[string][]json.RawMessage{}}
	for _, e := range entities {
		s.entities[e] = true
	}
	return s
}

// NewFileImportRowSink makes a file-rows run's buffer (ADR-0164 §2): the
// same caps, and the ask it is handed to runs in the import sandbox
// (FileImportHostFunctions only).
func NewFileImportRowSink(pluginID string, entities []string) *ImportRowSink {
	s := NewImportRowSink(pluginID, entities)
	s.file = true
	return s
}

type importRowSinkKey struct{}

// WithImportRowSink returns ctx carrying the run's sink; the WASM runtime
// hands it to the host state of the matching import ask only.
func WithImportRowSink(ctx context.Context, s *ImportRowSink) context.Context {
	return context.WithValue(ctx, importRowSinkKey{}, s)
}

// importRowSinkFor is the sink the host state gets for ev: only a
// core-raised (hop 0), non-job import ask for the sink's own plugin.
func importRowSinkFor(ctx context.Context, ev Event, isJob bool, pluginID string) *ImportRowSink {
	if ev.Hop != 0 || isJob || ev.Type != "import.requested.ask" {
		return nil
	}
	s, ok := ctx.Value(importRowSinkKey{}).(*ImportRowSink)
	if !ok || s == nil || s.pluginID != pluginID {
		return nil
	}
	return s
}

// Submit buffers one batch {"entity": "...", "rows": [...]}: it returns the
// rows accepted, or a negative hostErr* code.
func (s *ImportRowSink) Submit(raw []byte) (int, int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refused != nil {
		return 0, hostErrQuota
	}
	if s.bytes+int64(len(raw)) > ImportBytesPerRun {
		s.refused = fmt.Errorf("%w (%d bytes per run)", ErrImportRowsCap, ImportBytesPerRun)
		return 0, hostErrQuota
	}
	var b struct {
		Entity string            `json:"entity"`
		Rows   []json.RawMessage `json:"rows"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&b); err != nil || dec.More() || b.Entity == "" {
		return 0, hostErrInvalid
	}
	if !s.entities[b.Entity] {
		return 0, hostErrDenied
	}
	if len(b.Rows) > ImportRowsPerCall {
		s.refused = fmt.Errorf("%w (%d rows per call)", ErrImportRowsCap, ImportRowsPerCall)
		return 0, hostErrQuota
	}
	if s.n+len(b.Rows) > ImportRowsPerRun {
		s.refused = fmt.Errorf("%w (%d rows per run)", ErrImportRowsCap, ImportRowsPerRun)
		return 0, hostErrQuota
	}
	for _, r := range b.Rows {
		// Decode copies out of raw (a view into guest memory), so the
		// rows outlive this call.
		s.rows[b.Entity] = append(s.rows[b.Entity], r)
	}
	s.n += len(b.Rows)
	s.bytes += int64(len(raw))
	return len(b.Rows), 0
}

// Rows returns the buffered rows for entity, in submit order.
func (s *ImportRowSink) Rows(entity string) []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[entity]
}

// Len is how many rows the run buffered, across entities.
func (s *ImportRowSink) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// Refused is non-nil once a cap was breached: the run must not be applied.
func (s *ImportRowSink) Refused() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused
}

// hostImportRowsSubmit: import_rows_submit(ptr, len) → rows accepted, or
// -2 outside an API-source or file-rows import ask / for an unresolved entity, -4 for a
// malformed batch, -5 over a cap (the run is then refused).
func hostImportRowsSubmit(ctx context.Context, m api.Module, ptr, length uint32) int32 {
	s, ok := stateFromAny(ctx)
	if !ok {
		return hostErrInternal
	}
	if s.importRows == nil {
		logging.L().Infof("[wasm:%s] import_rows_submit denied: not inside an API-source or file-rows import run", s.pluginID)
		return hostErrDenied
	}
	if int64(length) > ImportBytesPerRun {
		s.importRows.mu.Lock()
		s.importRows.refused = fmt.Errorf("%w (%d bytes per run)", ErrImportRowsCap, ImportBytesPerRun)
		s.importRows.mu.Unlock()
		return hostErrQuota
	}
	raw, ok := readGuest(m, ptr, length)
	if !ok || len(raw) == 0 {
		return hostErrInvalid
	}
	n, code := s.importRows.Submit(raw)
	if code != 0 {
		// Ids and codes only — never row contents.
		logging.L().Infof("[wasm:%s] import_rows_submit refused (%d)", s.pluginID, code)
		return code
	}
	return int32(n)
}
