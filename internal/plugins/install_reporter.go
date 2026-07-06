package plugins

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// MarketplaceReporter reports POS-side install state transitions back to the
// marketplace install-intent endpoint (story 1-1, AC 5):
//
//	POST {baseURL}/ui/api/installs/{intentID}/state  (state=..., error=...)
//
// It is best-effort: reporting failures are logged but never fail an install.
type MarketplaceReporter struct {
	baseURL string
	token   string
	client  *http.Client
}

// NewMarketplaceReporter returns a reporter, or nil when the base URL is empty
// (reporting disabled). token authenticates the (token-gated) report endpoint.
func NewMarketplaceReporter(baseURL, token string) *MarketplaceReporter {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	return &MarketplaceReporter{
		baseURL: baseURL,
		token:   strings.TrimSpace(token),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Report posts a state (and optional error detail) for an install intent. A nil
// reporter or empty intentID is a no-op.
func (r *MarketplaceReporter) Report(ctx context.Context, intentID string, state InstallLifecycleState, errDetail string) {
	if r == nil || strings.TrimSpace(intentID) == "" {
		return
	}
	log := logging.L()
	form := url.Values{"state": {string(state)}}
	if errDetail != "" {
		form.Set("error", errDetail)
	}
	endpoint := fmt.Sprintf("%s/ui/api/installs/%s/state", r.baseURL, url.PathEscape(intentID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		log.Warnf("[InstallReporter] build request failed: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		log.Warnf("[InstallReporter] report %s for %s failed: %v", state, intentID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Warnf("[InstallReporter] report %s for %s: HTTP %d %s", state, intentID, resp.StatusCode, strings.TrimSpace(string(body)))
		return
	}
	log.Infof("[InstallReporter] reported %s for intent %s", state, intentID)
}
