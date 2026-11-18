package channelactions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	// maxAnswerBody holds 50 results, each with a `current` of up to 64 KiB.
	maxAnswerBody = 4 << 20
	maxErrorBody  = 4 << 10
)

// encodeBody is the request body: {"store_id", "actions"}.
func encodeBody(storeID string, actions []Action) ([]byte, error) {
	return json.Marshal(struct {
		StoreID string   `json:"store_id"`
		Actions []Action `json:"actions"`
	}{storeID, actions})
}

// NewHTTPPoster posts to POST <endpoint>/v1/stores/channel/orders/actions with
// the till's own per-device bearer credential (ADR-0116), as stockupload's
// poster does (endpoint already ends in /api). token is a secret: it is never
// put on an error or a log line. A 403 main_till_required, store_mismatch or
// device_credential_required is a StopError, a 400 a BadRequestError (a till
// bug), any other non-200 a StatusError; the rows are kept in every case.
func NewHTTPPoster(client *http.Client, endpoint, storeID, token string) Poster {
	url := strings.TrimRight(endpoint, "/") + "/v1/stores/channel/orders/actions"
	return func(ctx context.Context, actions []Action) ([]Result, error) {
		body, err := encodeBody(storeID, actions)
		if err != nil {
			return nil, fmt.Errorf("channelactions: encode actions: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, errors.New("channelactions: build request")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			// *url.Error carries the URL, never the header; keep only the cause.
			var ue interface{ Unwrap() error }
			if errors.As(err, &ue) && ue.Unwrap() != nil {
				return nil, fmt.Errorf("channelactions: %w", ue.Unwrap())
			}
			return nil, errors.New("channelactions: request failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
			code := errorCode(raw)
			switch {
			case resp.StatusCode == http.StatusForbidden && (code == "main_till_required" || code == "store_mismatch" || code == "device_credential_required"):
				return nil, &StopError{Status: resp.StatusCode, Code: code}
			case resp.StatusCode == http.StatusBadRequest:
				return nil, &BadRequestError{Code: code}
			}
			return nil, &StatusError{Status: resp.StatusCode, Code: code}
		}
		var env struct {
			Data *struct {
				Results []Result `json:"results"`
			} `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswerBody)).Decode(&env); err != nil {
			return nil, errors.New("channelactions: decode answers")
		}
		if env.Data == nil {
			return nil, errors.New("channelactions: answers without data")
		}
		return env.Data.Results, nil
	}
}

// errorCode reads ut-cloud's {error:{code}} envelope; "" when the body is not
// the envelope or the code is not a short [a-z0-9_] token, so a hostile body
// cannot put text in a log line.
func errorCode(body []byte) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil {
		return ""
	}
	code := env.Error.Code
	if code == "" || len(code) > 40 {
		return ""
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return ""
		}
	}
	return code
}
