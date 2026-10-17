package catalogpull

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// KeyFailuresReported is the fingerprint of the last apply-failures set the
// cloud accepted (ut-docs#4493). "cloudsync." is a per-till settings family
// (data.PerTillSettingPrefixes), so a replica never inherits the main
// till's report state.
const KeyFailuresReported = "cloudsync.catalog.failures_reported"

// KeyFailuresRejected is the fingerprint of the last set the cloud refused
// with a 400: the same body cannot succeed, so it is not re-posted until
// the set (or the scope) changes. Per-till like KeyFailuresReported.
const KeyFailuresRejected = "cloudsync.catalog.failures_rejected"

// fingerprintVersion prefixes both fingerprints; bumping it makes every
// till re-post its set once ("v2:" added the store scope).
const fingerprintVersion = "v2:"

// MaxReportedFailures is the most entries one report carries (contract
// cloud-catalog-feed.md, POST …/apply-failures).
const MaxReportedFailures = 500

// maxReportBody is the cloud's request-body limit; maxReportFailuresJSON
// leaves room for the envelope and the store id around the failures array.
const (
	maxReportBody         = 256 << 10
	maxReportFailuresJSON = maxReportBody - 4<<10
	maxReportReasonRunes  = 500
)

// reportKinds are the kinds the cloud accepts in a report.
var reportKinds = map[string]bool{
	"category": true, "item": true, "item_variant": true, "item_barcode": true,
	"modifier_group": true, "option_set": true, "layout": true, "tax_code": true,
	"staff": true, "setting": true,
}

// Failure is one entry of the apply-failures report: a cloud row the till
// refused, its version, why, and when (RFC 3339 UTC).
type Failure struct {
	Kind    string `json:"kind"`
	CloudID string `json:"cloud_id"`
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
	At      string `json:"at"`
}

// Reporter sends the till's whole current set of refused rows (newest
// first; empty clears the cloud's copy).
type Reporter func(ctx context.Context, failures []Failure) error

// ReportStopError is an answer the next tick will not change (401/403 —
// the till's credential is not accepted): the caller stays quiet and the
// fingerprint is kept. 409 is mapped here for forward compatibility only;
// the cloud's endpoint does not answer it today.
type ReportStopError struct {
	Status int
	Code   string
}

func (e *ReportStopError) Error() string {
	return fmt.Sprintf("catalogpull: apply-failures report answered %d %s", e.Status, e.Code)
}

// ReportRejectedError is a 400: the cloud refuses this body, and posting
// the same body again cannot succeed. ReportFailures records its
// fingerprint (KeyFailuresRejected) and posts again only once the set
// changes.
type ReportRejectedError struct {
	Status int
	Code   string
}

func (e *ReportRejectedError) Error() string {
	return fmt.Sprintf("catalogpull: apply-failures report answered %d %s", e.Status, e.Code)
}

// ReportStatusError is any other non-200 answer (503, …); the next tick
// retries.
type ReportStatusError struct {
	Status int
	Code   string
}

func (e *ReportStatusError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("catalogpull: apply-failures report answered %d", e.Status)
	}
	return fmt.Sprintf("catalogpull: apply-failures report answered %d %s", e.Status, e.Code)
}

// NewHTTPReporter posts to <endpoint>/v1/stores/catalog/apply-failures
// with the till's per-device bearer credential — the feed's base URL and
// Authorization header. token is a secret: it is never put on an error or
// a log line.
func NewHTTPReporter(client *http.Client, endpoint, storeID, token string) Reporter {
	target := strings.TrimRight(endpoint, "/") + "/v1/stores/catalog/apply-failures"
	return func(ctx context.Context, failures []Failure) error {
		if failures == nil {
			failures = []Failure{} // a clear is [], never null
		}
		body, err := json.Marshal(struct {
			StoreID  string    `json:"store_id"`
			Failures []Failure `json:"failures"`
		}{storeID, failures})
		if err != nil {
			return fmt.Errorf("catalogpull: encode apply-failures report: %w", err)
		}
		if len(body) > maxReportBody {
			return fmt.Errorf("catalogpull: apply-failures report is %d bytes, over the %d limit", len(body), maxReportBody)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		if resp.StatusCode == http.StatusOK {
			return nil // the 200 body ({"stored": n}) is read and discarded
		}
		code := errorCode(raw)
		switch resp.StatusCode {
		case http.StatusBadRequest:
			if code == "" {
				code = "http_400"
			}
			return &ReportRejectedError{Status: resp.StatusCode, Code: code}
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict:
			if code == "" {
				code = "http_" + strconv.Itoa(resp.StatusCode)
			}
			return &ReportStopError{Status: resp.StatusCode, Code: code}
		}
		return &ReportStatusError{Status: resp.StatusCode, Code: code}
	}
}

// ReportFailures sends the till's current refused rows (at most
// MaxReportedFailures, newest first) when they differ from the set the
// cloud last accepted, and reports whether it posted. Nothing is posted
// when the set is unchanged, or when it is empty and nothing was ever
// reported. scope names where the set goes (the store id and the cloud
// endpoint, newline-joined) and is part of the fingerprint, so a till
// moved to another store or cloud posts its set there too. The fingerprint
// moves only after an accepted post, so an error retries the same set on
// the next call — except a 400 (ReportRejectedError), whose set waits for
// a change.
func ReportFailures(ctx context.Context, db *sql.DB, scope string, post Reporter) (bool, error) {
	rows, err := data.NewCloudLinkRepo().ListFailures(ctx, db, MaxReportedFailures)
	if err != nil {
		return false, err
	}
	failures := reportable(rows)
	canon, err := json.Marshal(failures)
	if err != nil {
		return false, fmt.Errorf("catalogpull: encode apply failures: %w", err)
	}
	h := sha256.New()
	h.Write([]byte(scope))
	h.Write([]byte{0})
	h.Write(canon)
	fp := fingerprintVersion + hex.EncodeToString(h.Sum(nil))

	settings := data.NewSettingsRepo(db)
	last, _, err := settings.Get(ctx, KeyFailuresReported)
	if err != nil {
		return false, fmt.Errorf("catalogpull: read %s: %w", KeyFailuresReported, err)
	}
	if last == fp || (last == "" && len(failures) == 0) {
		return false, nil
	}
	rejected, _, err := settings.Get(ctx, KeyFailuresRejected)
	if err != nil {
		return false, fmt.Errorf("catalogpull: read %s: %w", KeyFailuresRejected, err)
	}
	if rejected == fp {
		return false, nil // the cloud refused this very body; wait for a change
	}
	if err := post(ctx, failures); err != nil {
		var rej *ReportRejectedError
		if errors.As(err, &rej) {
			if serr := settings.Set(ctx, KeyFailuresRejected, fp); serr != nil {
				return false, fmt.Errorf("catalogpull: store %s: %w (after %w)", KeyFailuresRejected, serr, err)
			}
		}
		return false, err
	}
	if err := settings.Set(ctx, KeyFailuresReported, fp); err != nil {
		return true, fmt.Errorf("catalogpull: store %s: %w", KeyFailuresReported, err)
	}
	if rejected != "" {
		if err := settings.Set(ctx, KeyFailuresRejected, ""); err != nil {
			return true, fmt.Errorf("catalogpull: clear %s: %w", KeyFailuresRejected, err)
		}
	}
	return true, nil
}

// reportable turns the recorded refusals into the report's entries,
// dropping any the cloud would refuse — one invalid entry fails the whole
// report, which would then never get through (a non-UUID id, for one, is
// only in the till's own log and its cloud_apply_failures row) — and
// trimming the oldest entries until the set fits the cloud's body limit.
// Never nil.
func reportable(rows []data.CloudApplyFailure) []Failure {
	out := make([]Failure, 0, len(rows))
	seen := make(map[[2]string]bool, len(rows))
	size := 2 // the array's brackets
	trimmed := 0
	for _, r := range rows {
		id, ok := canonicalUUID(r.CloudID)
		if !reportKinds[r.Entity] || !ok || r.Version < 1 || seen[[2]string{r.Entity, id}] {
			continue
		}
		at, err := time.Parse(time.RFC3339, r.At)
		if err != nil {
			continue
		}
		reason, ok := cleanReason(r.Reason)
		if !ok {
			continue
		}
		f := Failure{
			Kind: r.Entity, CloudID: id, Version: r.Version,
			Reason: reason, At: at.UTC().Format(time.RFC3339),
		}
		b, err := json.Marshal(f)
		if err != nil {
			continue
		}
		if trimmed > 0 || size+len(b)+1 > maxReportFailuresJSON {
			// Rows are newest first: the oldest are left out, and are not
			// sent until the set changes (the fingerprint covers what was
			// sent, so an unchanged set never posts them).
			trimmed++
			continue
		}
		seen[[2]string{r.Entity, id}] = true
		size += len(b) + 1
		out = append(out, f)
	}
	if trimmed > 0 {
		logging.L().Infof("catalogpull: apply-failures report left out the %d oldest entries to fit the cloud's body limit", trimmed)
	}
	return out
}

// cleanReason makes a reason that passes ut-cloud's
// cleanApplyFailureReason unchanged. The cloud checks 1–500 runes and no
// U+FFFD on the raw value, then strips control and format (Cf) characters
// and trims, refusing an empty result. Here: invalid UTF-8 and U+FFFD
// become '?' (the entry still reaches the cloud), control and Cf
// characters are stripped, the result is trimmed, cut to 500 runes and
// trimmed again — so the cloud's own strip and trim change nothing. false
// when nothing is left (the cloud would refuse the whole report).
func cleanReason(s string) (string, bool) {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, "?"), string(utf8.RuneError), "?")
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s))
	if utf8.RuneCountInString(s) > maxReportReasonRunes {
		s = strings.TrimSpace(string([]rune(s)[:maxReportReasonRunes]))
	}
	return s, s != ""
}

// canonicalUUID mirrors ut-cloud's canonicalUUID: the 8-4-4-4-12 form in
// any hex case, never the nil UUID. It returns the id lower-cased.
func canonicalUUID(s string) (string, bool) {
	if len(s) != 36 {
		return "", false
	}
	nonZero := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return "", false
			}
		default:
			switch {
			case c == '0':
			case (c >= '1' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
				nonZero = true
			default:
				return "", false
			}
		}
	}
	if !nonZero {
		return "", false
	}
	return strings.ToLower(s), true
}
