package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/customerimport"
	"github.com/universaltill/universal-till/internal/directivekey"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// maxImportCustomersPlaintext is the most plaintext one chunk may open to
// (ADR-0168 §5: 64 KiB). The sealed form is a little larger.
const maxImportCustomersPlaintext = 64 * 1024

// Owner-readable refusals, English like every directive result.
const (
	msgImportCustomersKeyGone = "These customers were sealed to a till key that no longer exists. Upload the file again."
	msgImportCustomersBad     = "The customer rows could not be read."
)

// cloudImportCustomers is the sealed apply entry point of ADR-0168 §5
// (ut-docs#4748): it opens one import_customers chunk with the main till's
// directive key (bound to this store, directive, job and chunk), decodes it
// and applies it through customerimport. Main till only: a satellite
// refuses (customers are primary-wins synced to it). The result carries
// counts and per-row outcomes with field names, never a customer value, and
// nothing about the rows reaches a log.
func cloudImportCustomers(ctx context.Context, d *common.Deps, keys *directivekey.Store, dir cloudsync.ImportCustomersDirective) (cloudsync.ImportCustomersResult, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return cloudsync.ImportCustomersResult{}, err
	}
	if len(dir.Sealed) > 2*maxImportCustomersPlaintext {
		return cloudsync.ImportCustomersResult{}, errors.New("The customer rows are larger than a chunk may be.")
	}
	storeID := enroll.Effective(d.Cfg).Marketplace.StoreID
	pt, err := keys.OpenSealed(dir.Sealed, directivekey.CustomersInfo,
		directivekey.CustomersAAD(storeID, dir.DirectiveID, "import_customers", dir.JobID, dir.ChunkIndex))
	if err != nil {
		if errors.Is(err, directivekey.ErrMalformed) {
			return cloudsync.ImportCustomersResult{}, errors.New(msgImportCustomersBad)
		}
		return cloudsync.ImportCustomersResult{}, errors.New(msgImportCustomersKeyGone)
	}
	defer func() {
		for i := range pt {
			pt[i] = 0
		}
	}()
	if len(pt) > maxImportCustomersPlaintext {
		return cloudsync.ImportCustomersResult{}, errors.New("The customer rows are larger than a chunk may be.")
	}
	var req customerimport.Request
	if err := json.Unmarshal(pt, &req); err != nil {
		// The decoder's error can quote input: say nothing about it.
		return cloudsync.ImportCustomersResult{}, errors.New(msgImportCustomersBad)
	}
	req.JobID = dir.JobID
	res, err := customerimport.Apply(ctx, d.Db, req, "system")
	if err != nil {
		return cloudsync.ImportCustomersResult{}, err
	}
	// Round-trip through JSON for a plain map (the result's shape is the
	// contract; nothing else is added to it).
	raw, _ := json.Marshal(res)
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	return cloudsync.ImportCustomersResult{Message: importCustomersMessage(res.Counts), Data: data}, nil
}

// importCustomersMessage is the result line: outcome counts, nothing else.
func importCustomersMessage(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], strings.ReplaceAll(k, "_", " ")))
	}
	if len(parts) == 0 {
		return "no customers in this chunk"
	}
	return strings.Join(parts, ", ")
}
