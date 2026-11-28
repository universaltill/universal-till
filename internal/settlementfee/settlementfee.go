// Package settlementfee reads the provider fee lines the cloud supplies in
// its till settlement summary (ut-docs#7160, epic #7061) so the day-close
// screen can show them. It is display-only: the till never computes, rounds
// or caches a fee. Whatever the cloud imported from the payment provider's
// balance transactions is shown as is; no data, a failure or an unknown shape
// yields no lines, and the caller renders nothing.
package settlementfee

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

const (
	// Timeout bounds the whole fetch: the day-close screen must never wait
	// on the network.
	Timeout = 3 * time.Second
	// MaxLines bounds how many rows one day can show.
	MaxLines = 8
	// maxBody bounds the answer read.
	maxBody = 64 << 10
)

// Line is one fee row to show. LocaleKey is a key of web/locales/en.json
// chosen by this package, never text taken from the cloud.
type Line struct {
	Provider  string
	LocaleKey string
	Currency  string
	Fee       money.Money
}

// Fetcher returns the fee lines for a local calendar day (YYYY-MM-DD).
type Fetcher func(ctx context.Context, date string) ([]Line, error)

// The contract's label_key ("day_close.<name>") selects the till's own
// locale key "reports.eod.<name>". The cloud supplies no display text; a
// label_key that is not of that shape, or whose locale key the till does not
// have (Has), is dropped. The package names no provider (core neutrality,
// ADR-0121 §11): which fees exist is the cloud's data and the locale pack's.
const (
	labelPrefix  = "day_close."
	localePrefix = "reports.eod."
)

var (
	nameRe     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	providerRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	dateRe     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
	idRe       = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

type summary struct {
	Currency string `json:"currency"`
	Lines    []struct {
		Provider string `json:"provider"`
		LabelKey string `json:"label_key"`
		FeeMinor int64  `json:"fee_minor"`
	} `json:"provider_fee_lines"`
}

// Has reports whether the till has a locale key; set by the caller (pages)
// from the loaded English pack. A nil Has drops every line.
type Has func(key string) bool

// Parse reads a settlement-summary answer (bare, or inside the {"data":..}
// envelope) and returns the displayable lines: lines with a known
// label key and a positive amount, at most MaxLines. Anything else gives no
// lines.
func Parse(body []byte, has Has) ([]Line, error) {
	var env struct {
		Data *summary `json:"data"`
	}
	var s summary
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	if env.Data != nil {
		s = *env.Data
	} else if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	if !currencyRe.MatchString(s.Currency) {
		return nil, nil
	}
	var out []Line
	for _, l := range s.Lines {
		name, ok := strings.CutPrefix(l.LabelKey, labelPrefix)
		if !ok || !nameRe.MatchString(name) || !providerRe.MatchString(l.Provider) || l.FeeMinor <= 0 {
			continue
		}
		key := localePrefix + name
		if has == nil || !has(key) {
			continue
		}
		if len(out) == MaxLines {
			break
		}
		out = append(out, Line{Provider: l.Provider, LocaleKey: key, Currency: s.Currency, Fee: money.FromMinor(l.FeeMinor)})
	}
	return out, nil
}

// NewHTTPFetcher reads GET <endpoint>/v1/tills/{till}/settlement-summary
// ?date=YYYY-MM-DD with the till's own bearer credential. token is a secret:
// it never appears on an error or a log line. Nothing is cached.
func NewHTTPFetcher(client *http.Client, endpoint, tillID, token string, has Has) Fetcher {
	base := strings.TrimRight(endpoint, "/")
	return func(ctx context.Context, date string) ([]Line, error) {
		if !dateRe.MatchString(date) || !idRe.MatchString(tillID) {
			return nil, errors.New("settlementfee: invalid date or till id")
		}
		ctx, cancel := context.WithTimeout(ctx, Timeout)
		defer cancel()
		u := base + "/v1/tills/" + url.PathEscape(tillID) + "/settlement-summary?date=" + url.QueryEscape(date)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, errors.New("settlementfee: request failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			return nil, fmt.Errorf("settlementfee: summary answered %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			return nil, err
		}
		return Parse(body, has)
	}
}
