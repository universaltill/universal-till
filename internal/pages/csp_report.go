package pages

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/universaltill/universal-till/internal/logging"
)

// Content-Security-Policy, report-only (ut-docs#2913, slice 1).
//
// universal-till sends no CSP on its own HTML. Before a later slice enforces
// one, UT_CSP_REPORT_ONLY=1 sends this policy as
// Content-Security-Policy-Report-Only on every response and collects what
// browsers report at /csp-report, so the violations an enforced policy
// would cause (inline scripts, inline styles, …) can be inventoried first.
// With the flag off (the default) neither the header nor the route exists.
// Plugin pages keep their own enforced pluginPageCSP (plugin_page.go) on
// top of this.

// cspReportOnlyPolicy is the policy under evaluation. 'report-sample' makes
// browsers include the first characters of a blocked inline script.
const cspReportOnlyPolicy = "default-src 'self'; script-src 'self' 'report-sample'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'; report-uri /csp-report"

const (
	// cspReportMaxBody caps one POST /csp-report body (413 above it).
	cspReportMaxBody = 64 << 10
	// cspReportMaxEntries bounds the unique-violation set; past it new
	// violations are only counted as dropped.
	cspReportMaxEntries = 1000
	// cspReportMaxFieldRunes caps every stored/logged string field.
	cspReportMaxFieldRunes = 256
)

// cspReportOnlyMiddleware sets the report-only header and calls next with
// the ResponseWriter it was given: never a wrapper, so http.Flusher (SSE)
// and http.Hijacker keep working on every route.
func cspReportOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy-Report-Only", cspReportOnlyPolicy)
		next.ServeHTTP(w, r)
	})
}

// cspViolation is one sanitised report, also the dedupe key.
type cspViolation struct {
	EffectiveDirective string
	BlockedURI         string
	DocumentPath       string
	SourceFile         string
	Line               int
}

// cspEntry is one row of the GET /csp-report inventory.
type cspEntry struct {
	EffectiveDirective string `json:"effective_directive"`
	BlockedURI         string `json:"blocked_uri"`
	DocumentPath       string `json:"document_path"`
	SourceFile         string `json:"source_file"`
	Line               int    `json:"line"`
	Sample             string `json:"sample"`
	Count              int    `json:"count"`
}

// cspReportStore is the bounded, in-memory set of unique violations. It
// lives only for the process: this is an inventory aid, not till data.
type cspReportStore struct {
	mu      sync.Mutex
	max     int
	entries map[cspViolation]*cspEntry
	dropped int
}

func newCSPReportStore(max int) *cspReportStore {
	return &cspReportStore{max: max, entries: map[cspViolation]*cspEntry{}}
}

// add records one violation and reports whether it was new (and stored).
func (s *cspReportStore) add(v cspViolation, sample string) (isNew, capReached bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[v]; ok {
		e.Count++
		return false, false
	}
	if len(s.entries) >= s.max {
		s.dropped++
		return false, s.dropped == 1
	}
	s.entries[v] = &cspEntry{
		EffectiveDirective: v.EffectiveDirective,
		BlockedURI:         v.BlockedURI,
		DocumentPath:       v.DocumentPath,
		SourceFile:         v.SourceFile,
		Line:               v.Line,
		Sample:             sample,
		Count:              1,
	}
	return true, false
}

// snapshot returns a copy of every entry, sorted by its key fields so the
// order is deterministic.
func (s *cspReportStore) snapshot() []cspEntry {
	s.mu.Lock()
	out := make([]cspEntry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, *e)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.EffectiveDirective != b.EffectiveDirective:
			return a.EffectiveDirective < b.EffectiveDirective
		case a.DocumentPath != b.DocumentPath:
			return a.DocumentPath < b.DocumentPath
		case a.BlockedURI != b.BlockedURI:
			return a.BlockedURI < b.BlockedURI
		case a.SourceFile != b.SourceFile:
			return a.SourceFile < b.SourceFile
		default:
			return a.Line < b.Line
		}
	})
	return out
}

// legacyCSPReport is the report-uri body (Content-Type application/csp-report).
type legacyCSPReport struct {
	Report *struct {
		DocumentURI        string      `json:"document-uri"`
		ViolatedDirective  string      `json:"violated-directive"`
		EffectiveDirective string      `json:"effective-directive"`
		BlockedURI         string      `json:"blocked-uri"`
		SourceFile         string      `json:"source-file"`
		LineNumber         json.Number `json:"line-number"`
		ColumnNumber       json.Number `json:"column-number"`
		ScriptSample       string      `json:"script-sample"`
	} `json:"csp-report"`
}

// reportingAPIReport is one entry of a Reporting API body
// (Content-Type application/reports+json).
type reportingAPIReport struct {
	Type string `json:"type"`
	URL  string `json:"url"`
	Body struct {
		DocumentURL        string      `json:"documentURL"`
		EffectiveDirective string      `json:"effectiveDirective"`
		BlockedURL         string      `json:"blockedURL"`
		SourceFile         string      `json:"sourceFile"`
		LineNumber         json.Number `json:"lineNumber"`
		ColumnNumber       json.Number `json:"columnNumber"`
		Sample             string      `json:"sample"`
	} `json:"body"`
}

// rawCSPViolation is either wire format, before sanitising.
type rawCSPViolation struct {
	documentURI, effectiveDirective, violatedDirective string
	blockedURI, sourceFile, sample                     string
	line                                               json.Number
}

var errCSPReportShape = errors.New("not a CSP violation report")

func parseCSPReports(mediaType string, body []byte) ([]rawCSPViolation, error) {
	switch mediaType {
	case "application/csp-report":
		var rep legacyCSPReport
		if err := json.Unmarshal(body, &rep); err != nil {
			return nil, err
		}
		if rep.Report == nil {
			return nil, errCSPReportShape
		}
		r := rep.Report
		return []rawCSPViolation{{
			documentURI: r.DocumentURI, effectiveDirective: r.EffectiveDirective,
			violatedDirective: r.ViolatedDirective, blockedURI: r.BlockedURI,
			sourceFile: r.SourceFile, sample: r.ScriptSample, line: r.LineNumber,
		}}, nil
	default: // application/reports+json
		var reps []reportingAPIReport
		if err := json.Unmarshal(body, &reps); err != nil {
			return nil, err
		}
		var out []rawCSPViolation
		for _, r := range reps {
			if r.Type != "csp-violation" {
				continue
			}
			doc := r.Body.DocumentURL
			if doc == "" {
				doc = r.URL
			}
			out = append(out, rawCSPViolation{
				documentURI: doc, effectiveDirective: r.Body.EffectiveDirective,
				blockedURI: r.Body.BlockedURL, sourceFile: r.Body.SourceFile,
				sample: r.Body.Sample, line: r.Body.LineNumber,
			})
		}
		return out, nil
	}
}

// cspSanitize strips control and line-separator characters (no forged log
// lines), repairs invalid UTF-8 and caps the length in runes.
func cspSanitize(s string) string {
	s = strings.ToValidUTF8(s, "�")
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			continue
		}
		if n == cspReportMaxFieldRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// cspStripQuery drops the query string and fragment, which may carry
// tokens or other secrets.
func cspStripQuery(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		return s[:i]
	}
	return s
}

// cspDocumentPath reduces a document URL to its path.
func cspDocumentPath(s string) string {
	s = cspStripQuery(s)
	if u, err := url.Parse(s); err == nil && (u.Scheme != "" || u.Host != "") {
		if u.Path == "" {
			return "/"
		}
		return u.Path
	}
	return s
}

func (raw rawCSPViolation) sanitize() (cspViolation, string) {
	directive := raw.effectiveDirective
	if directive == "" {
		// Older browsers send only violated-directive ("img-src 'self'").
		directive, _, _ = strings.Cut(strings.TrimSpace(raw.violatedDirective), " ")
	}
	line, err := strconv.Atoi(raw.line.String())
	if err != nil || line < 0 {
		line = 0
	}
	return cspViolation{
		EffectiveDirective: cspSanitize(directive),
		BlockedURI:         cspSanitize(cspStripQuery(raw.blockedURI)),
		DocumentPath:       cspSanitize(cspDocumentPath(raw.documentURI)),
		SourceFile:         cspSanitize(cspStripQuery(raw.sourceFile)),
		Line:               line,
	}, cspSanitize(raw.sample)
}

func writeCSPError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"data": nil, "error": map[string]string{"code": code, "message": msg}})
}

// registerCSPReport registers the violation sink and its inventory; only
// called when UT_CSP_REPORT_ONLY is on. POST is auth-exempt
// (auth.exemptRequest); GET stays behind the session.
func registerCSPReport(mux *http.ServeMux, store *cspReportStore, canRead func(*http.Request) bool) {
	mux.HandleFunc("POST /csp-report", func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || (mediaType != "application/csp-report" && mediaType != "application/reports+json") {
			writeCSPError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "expected application/csp-report or application/reports+json")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cspReportMaxBody))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeCSPError(w, http.StatusRequestEntityTooLarge, "too_large", "report body too large")
				return
			}
			writeCSPError(w, http.StatusBadRequest, "bad_request", "unreadable report body")
			return
		}
		if len(bytes.TrimSpace(body)) == 0 {
			writeCSPError(w, http.StatusBadRequest, "bad_request", "empty report body")
			return
		}
		reports, err := parseCSPReports(mediaType, body)
		if err != nil {
			writeCSPError(w, http.StatusBadRequest, "bad_request", "malformed report")
			return
		}
		// Validate before storing anything: a report with no directive tells
		// slices 2–4 nothing and would only pad the bounded set.
		type cleaned struct {
			v      cspViolation
			sample string
		}
		valid := make([]cleaned, 0, len(reports))
		for _, raw := range reports {
			v, sample := raw.sanitize()
			if v.EffectiveDirective == "" {
				writeCSPError(w, http.StatusBadRequest, "bad_request", "report has no directive")
				return
			}
			valid = append(valid, cleaned{v, sample})
		}
		for _, c := range valid {
			v, sample := c.v, c.sample
			isNew, capReached := store.add(v, sample)
			if isNew {
				// Info, not Warn: the Problems ring (50 lines) feeds bug
				// reports and the heartbeat, and one page load of today's
				// UI yields more unique violations than that. The GET
				// inventory is the designed output; this line is a trace.
				logging.L().Infof("csp-report: new violation directive=%q blocked=%q document=%q source=%q line=%d",
					v.EffectiveDirective, v.BlockedURI, v.DocumentPath, v.SourceFile, v.Line)
			}
			if capReached {
				logging.L().Warnf("csp-report: %d unique violations stored; further new ones are only counted", store.max)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /csp-report", func(w http.ResponseWriter, r *http.Request) {
		if !canRead(r) {
			writeCSPError(w, http.StatusForbidden, "forbidden", "manager or admin required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": store.snapshot(), "error": nil})
	})
}
