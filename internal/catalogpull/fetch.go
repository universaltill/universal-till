package catalogpull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// maxPageBody bounds one feed answer. A page is at most 500 rows and about
// 1 MiB, except one version too big for a page, which the cloud sends whole
// (contract "Paging guarantees" 1); 16 MiB leaves room for that and still
// bounds a misbehaving endpoint.
const maxPageBody = 16 << 20

// maxErrorBody bounds an error answer's read.
const maxErrorBody = 4 << 10

// StopError is an answer the next tick will not change: a 409
// (catalog_master_till, catalog_adopting — the cloud does not serve this
// store's catalogue now) or a 401/403 (device_credential_required — this
// till's credential is not accepted). The pull stops for this trigger with
// its cursor unchanged and no error, so a check-in tick does not log a
// retry failure every time.
type StopError struct {
	Status int
	Code   string
}

func (e *StopError) Error() string {
	return fmt.Sprintf("catalogpull: feed answered %d %s", e.Status, e.Code)
}

// ErrResync is the feed's 410 resync: pull everything again from since=0
// and prune what the full pull did not return.
var ErrResync = errors.New("catalogpull: the cloud asked for a resync from since=0")

// StatusError is any other non-200 answer (400, 503, …): the cursor is
// kept and the next trigger retries.
type StatusError struct {
	Status int
	Code   string
}

func (e *StatusError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("catalogpull: feed answered %d", e.Status)
	}
	return fmt.Sprintf("catalogpull: feed answered %d %s", e.Status, e.Code)
}

// NewHTTPFetcher reads GET <endpoint>/v1/stores/catalog (contract
// cloud-catalog-feed.md) with the till's own per-device bearer credential,
// the same base URL and Authorization header every other /v1/stores/*
// call of internal/cloudsync uses. token is a secret: it is never put on
// an error or a log line.
func NewHTTPFetcher(client *http.Client, endpoint, storeID, token string) Fetcher {
	base := strings.TrimRight(endpoint, "/") + "/v1/stores/catalog"
	return func(ctx context.Context, since int64, limit int) (Page, error) {
		q := url.Values{}
		q.Set("store_id", storeID)
		q.Set("since", strconv.FormatInt(since, 10))
		q.Set("limit", strconv.Itoa(limit))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
		if err != nil {
			return Page{}, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return Page{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
			code := errorCode(body)
			switch resp.StatusCode {
			case http.StatusConflict, http.StatusUnauthorized, http.StatusForbidden:
				if code == "" {
					code = "http_" + strconv.Itoa(resp.StatusCode)
				}
				return Page{}, &StopError{Status: resp.StatusCode, Code: code}
			case http.StatusGone:
				return Page{}, ErrResync
			}
			return Page{}, &StatusError{Status: resp.StatusCode, Code: code}
		}
		var env struct {
			Data *Page `json:"data"`
		}
		dec := json.NewDecoder(io.LimitReader(resp.Body, maxPageBody))
		if err := dec.Decode(&env); err != nil {
			return Page{}, fmt.Errorf("catalogpull: decode feed page: %w", err)
		}
		if env.Data == nil {
			return Page{}, errors.New("catalogpull: feed page without data")
		}
		return *env.Data, nil
	}
}

// errorCode reads ut-cloud's {error:{code}} envelope; "" when the body is
// not the envelope.
func errorCode(body []byte) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil {
		return ""
	}
	return env.Error.Code
}
