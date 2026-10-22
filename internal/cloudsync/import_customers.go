package cloudsync

import (
	"context"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/logging"
)

// import_customers (ADR-0168 §5, ut-docs#4748): up to 200 customer rows,
// HPKE-sealed by the cloud to this till's directive key. Payload: job_id,
// chunk_index and sealed (the v1 wire form). Only the main till applies it
// (mainTillOnlyTypes). This package never opens the seal and never logs it:
// the hook (pages.cloudImportCustomers) opens it, applies it and returns
// counts and per-row outcomes, which ride back as the result's data object.

// ImportCustomersDirective is a decoded import_customers directive.
type ImportCustomersDirective struct {
	DirectiveID string
	JobID       string
	ChunkIndex  int
	Sealed      string
}

// ImportCustomersResult is what the hook reports: a message holding counts
// only, and the data object (per-row outcomes and field names, never a
// customer value).
type ImportCustomersResult struct {
	Message string
	Data    map[string]any
}

// maxImportCustomersChunkIndex bounds chunk_index: 20 000 rows in 200-row
// chunks is 100 chunks; a generous ceiling still refuses a nonsense value.
const maxImportCustomersChunkIndex = 100_000

// decodeImportCustomers reads the payload. A non-empty msg is the failure
// text; it never contains the sealed value.
func decodeImportCustomers(d directive) (ImportCustomersDirective, string) {
	out := ImportCustomersDirective{DirectiveID: d.ID}
	p := payload(d.Payload)
	job, ok := p.optStr("job_id")
	if !ok || job == nil || *job == "" {
		return out, "missing job_id"
	}
	out.JobID = *job
	sealed, ok := p.optStr("sealed")
	if !ok || sealed == nil || *sealed == "" {
		return out, "missing sealed"
	}
	out.Sealed = *sealed
	idx, ok := d.Payload["chunk_index"].(float64)
	if !ok || idx < 0 || idx > maxImportCustomersChunkIndex || idx != float64(int(idx)) {
		return out, "invalid chunk_index"
	}
	out.ChunkIndex = int(idx)
	return out, ""
}

// handleImportCustomers applies one import_customers directive and posts its
// result. Re-delivery is safe: the apply is idempotent (hashed baselines).
// A result the cloud did not acknowledge is retried by the next tick, which
// serves the directive again.
func handleImportCustomers(ctx context.Context, cfg *config.Config, d directive, hooks Hooks) {
	status, msg, data := "failed", "", map[string]any(nil)
	switch dir, bad := decodeImportCustomers(d); {
	case hooks.ImportCustomers == nil:
		msg = "import_customers is not supported on this till"
	case bad != "":
		msg = bad
	default:
		res, err := hooks.ImportCustomers(ctx, dir)
		if err != nil {
			msg = err.Error()
		} else {
			status, msg, data = "applied", res.Message, res.Data
		}
	}
	if err := postResult(ctx, cfg, d.ID, status, strings.TrimSpace(msg), data); err != nil {
		logging.L().Warnf("cloudsync: result for %s not delivered: %v", d.ID, err)
		return
	}
	logging.L().Infof("cloudsync: directive %s (import_customers) %s", d.ID, status)
}
