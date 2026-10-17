package catalogpull

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// maxAnswerBody bounds one change upload's answer: 100 answers, each with
// an item row and its barcodes.
const maxAnswerBody = 8 << 20

// NewHTTPPoster posts to POST <endpoint>/v1/stores/catalog/changes
// (contract cloud-catalog-changes.md) with the till's own per-device bearer
// credential, as NewHTTPFetcher reads the feed. token is a secret: it is
// never put on an error or a log line. A 409 (catalog_master_till,
// catalog_adopting), 402 (subscription_read_only) or 401/403 stops the
// upload (StopError); any other non-200 is a StatusError and the outbox is
// re-sent on the next trigger.
func NewHTTPPoster(client *http.Client, endpoint, storeID, token string) Poster {
	url := strings.TrimRight(endpoint, "/") + "/v1/stores/catalog/changes"
	return func(ctx context.Context, changes []Change) ([]Answer, error) {
		body, err := json.Marshal(map[string]any{"store_id": storeID, "changes": changes})
		if err != nil {
			return nil, fmt.Errorf("catalogpull: encode changes: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
			code := errorCode(raw)
			switch resp.StatusCode {
			case http.StatusConflict, http.StatusPaymentRequired, http.StatusUnauthorized, http.StatusForbidden:
				if code == "" {
					code = "http_" + strconv.Itoa(resp.StatusCode)
				}
				return nil, &StopError{Status: resp.StatusCode, Code: code}
			}
			return nil, &StatusError{Status: resp.StatusCode, Code: code}
		}
		var env struct {
			Data *struct {
				Results []Answer `json:"results"`
			} `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswerBody)).Decode(&env); err != nil {
			return nil, fmt.Errorf("catalogpull: decode change answers: %w", err)
		}
		if env.Data == nil {
			return nil, errors.New("catalogpull: change answers without data")
		}
		return env.Data.Results, nil
	}
}
