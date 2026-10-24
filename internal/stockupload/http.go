package stockupload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
)

const (
	maxAnswerBody = 1 << 20
	maxErrorBody  = 4 << 10
)

// NewHTTPPoster posts to POST <endpoint>/v1/stores/stock/levels with the
// till's own per-device bearer credential, as catalogpull's poster does
// (endpoint already ends in /api). token is a secret: it is never put on an
// error or a log line. A 400, 401, 402, 403, 404, 405 or 409 is a StopError
// (the credential, the store's mode, the till's version or role, or a cloud
// that does not serve stock yet: retrying at once cannot change it); any
// other non-200 is a StatusError and the marks are re-sent on the next
// trigger.
func NewHTTPPoster(client *http.Client, endpoint, storeID, token string) Poster {
	url := strings.TrimRight(endpoint, "/") + "/v1/stores/stock/levels"
	return func(ctx context.Context, r Request) ([]Answer, error) {
		body, err := json.Marshal(struct {
			StoreID         string `json:"store_id"`
			StreamID        string `json:"stream_id"`
			StreamStartedAt string `json:"stream_started_at"`
			TillVersion     string `json:"till_version"`
			Rows            []Row  `json:"rows"`
		}{storeID, r.StreamID, r.StreamStartedAt.UTC().Format(data.StreamStartLayout), r.TillVersion, r.Rows})
		if err != nil {
			return nil, fmt.Errorf("stockupload: encode rows: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, errors.New("stockupload: build request")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			// *url.Error carries the URL, never the header; keep only the cause.
			var ue interface{ Unwrap() error }
			if errors.As(err, &ue) && ue.Unwrap() != nil {
				return nil, fmt.Errorf("stockupload: %w", ue.Unwrap())
			}
			return nil, errors.New("stockupload: request failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
			code := errorCode(raw)
			switch resp.StatusCode {
			case http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden,
				http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusConflict:
				if code == "" {
					code = fmt.Sprintf("http_%d", resp.StatusCode)
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
			return nil, errors.New("stockupload: decode stock answers")
		}
		if env.Data == nil {
			return nil, errors.New("stockupload: stock answers without data")
		}
		return env.Data.Results, nil
	}
}

// errorCode reads ut-cloud's {error:{code}} envelope; "" when the body is
// not the envelope. The code is cut to a short printable token so a hostile
// body cannot put text in a log line.
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
	if len(code) > 40 {
		return ""
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return ""
		}
	}
	return code
}
