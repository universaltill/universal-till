package data

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Core read views (ADR-0121 §5, ut-docs#3158): the only way a WASM plugin
// reads core data. Each view is a named, versioned, read-only query over an
// existing repository method, gated by a view:<class> permission and the
// plugin's manifest views_used, with bounded integer arguments and a byte
// cap on the JSON result. The contract is ut-docs
// reference/contracts/plugin-views.md: a changed argument or result shape is
// a new .vN, and the old one stays until no signed plugin lists it.
//
// The registry below is the ADR's "core table": the views are code, so they
// are registered in code rather than in a database table.

// CoreViewDeadline bounds one view query (ADR-0121 §5).
const CoreViewDeadline = 5 * time.Second

// CoreViewMaxResult is the largest JSON result a view returns (ADR-0121 §5).
const CoreViewMaxResult = 256 << 10

// ErrCoreViewTooLarge: the view's JSON result is over the caller's byte cap.
// Never truncated — a partial aggregate would read as a complete one.
var ErrCoreViewTooLarge = errors.New("core view result over its byte cap")

// CoreViewArg is one integer argument with inclusive bounds. A missing
// argument takes Default; anything outside [Min, Max] is refused.
type CoreViewArg struct {
	Name     string
	Min, Max int
	Default  int
}

// CoreView is one registered read view.
type CoreView struct {
	Name       string // versioned, e.g. sales.by_day.v1
	Permission string // view:<class>
	Args       []CoreViewArg
	// Run executes the view. args holds every declared argument, already
	// bounds-checked by ParseArgs. The result marshals to a JSON array.
	Run func(ctx context.Context, db *sql.DB, args map[string]int) (any, error)
}

// ParseArgs validates a view's JSON argument object: empty input or null
// means all defaults; otherwise a JSON object whose keys are declared
// arguments and whose values are integers within bounds. Anything else —
// an unknown key, a string, a fraction, an out-of-range value — is an
// error, so a plugin learns it asked for something it can't have instead
// of silently getting a default.
func (v CoreView) ParseArgs(raw []byte) (map[string]int, error) {
	var in map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		if err := dec.Decode(&in); err != nil {
			return nil, fmt.Errorf("view %s: arguments must be a JSON object: %w", v.Name, err)
		}
		if _, err := dec.Token(); err != io.EOF {
			return nil, fmt.Errorf("view %s: trailing data after the arguments object", v.Name)
		}
	}
	out := make(map[string]int, len(v.Args))
	declared := make(map[string]CoreViewArg, len(v.Args))
	for _, a := range v.Args {
		declared[a.Name] = a
		out[a.Name] = a.Default
	}
	for k, rawVal := range in {
		a, ok := declared[k]
		if !ok {
			return nil, fmt.Errorf("view %s: unknown argument %q", v.Name, k)
		}
		n, err := strictInt(rawVal)
		if err != nil {
			return nil, fmt.Errorf("view %s: argument %s: %w", v.Name, k, err)
		}
		if n < int64(a.Min) || n > int64(a.Max) {
			return nil, fmt.Errorf("view %s: argument %s = %d is outside %d-%d", v.Name, k, n, a.Min, a.Max)
		}
		out[k] = int(n)
	}
	return out, nil
}

// strictInt accepts a JSON number with no fractional part (7 or 7.0), and
// no exponent (1e2 is refused, so the contract's "integer" means one form).
func strictInt(raw json.RawMessage) (int64, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || (s[0] != '-' && (s[0] < '0' || s[0] > '9')) || strings.ContainsAny(s, "eE") {
		return 0, fmt.Errorf("must be an integer")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) || math.Abs(f) > 1<<31 {
		return 0, fmt.Errorf("must be an integer")
	}
	return int64(f), nil
}

// RunCoreView runs v under CoreViewDeadline and returns its JSON result —
// "[]" when nothing matches — or ErrCoreViewTooLarge when the result is
// over maxBytes.
func RunCoreView(ctx context.Context, db *sql.DB, v CoreView, args map[string]int, maxBytes int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, CoreViewDeadline)
	defer cancel()
	res, err := v.Run(ctx, db, args)
	if err != nil {
		return nil, err
	}
	if rv := reflect.ValueOf(res); res == nil || (rv.Kind() == reflect.Slice && rv.IsNil()) {
		res = []struct{}{}
	}
	out, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("view %s: encode result: %w", v.Name, err)
	}
	if len(out) > maxBytes {
		return nil, ErrCoreViewTooLarge
	}
	return out, nil
}

// LookupCoreView returns the registered view called name.
func LookupCoreView(name string) (CoreView, bool) {
	v, ok := coreViews[name]
	return v, ok
}

// businessDayStartRe matches a "reports.business_day_start" value — the same
// HH:MM pattern the EOD schedule time uses (pages' eodTimeRe).
var businessDayStartRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// ParseBusinessDayStart parses a "reports.business_day_start" setting value
// ("HH:MM"). Empty or malformed input defaults to midnight (0, 0) —
// calendar-midnight behaviour, unchanged from before this setting existed.
func ParseBusinessDayStart(hhmm string) (hour, minute int) {
	if hhmm == "" || !businessDayStartRe.MatchString(hhmm) {
		return 0, 0
	}
	h, err1 := strconv.Atoi(hhmm[:2])
	m, err2 := strconv.Atoi(hhmm[3:])
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return h, m
}

// lastDays is the rolling [from, to) window ending now that the Ask tools
// use (pages' daysArgWindow): +1s pad because SQL window comparisons
// truncate to whole seconds.
func lastDays(days int) (time.Time, time.Time) {
	to := time.Now().Add(time.Second)
	return to.Add(-time.Duration(days) * 24 * time.Hour), to
}

var (
	argDays  = CoreViewArg{Name: "days", Min: 1, Max: 365, Default: 14}
	argLimit = CoreViewArg{Name: "limit", Min: 1, Max: 50, Default: 10}
)

// auditSummaryRows is the Ask tool's fixed row cap for audit.summary.v1.
const auditSummaryRows = 100

// coreViews is the first set (ADR-0121 §5) — today's Ask tools
// (internal/pages/ask_api.go), same arguments, bounds and caps.
var coreViews = map[string]CoreView{
	"sales.by_day.v1": {
		Name: "sales.by_day.v1", Permission: "view:sales", Args: []CoreViewArg{argDays},
		Run: func(ctx context.Context, db *sql.DB, args map[string]int) (any, error) {
			v, _, err := NewSettingsRepo(db).Get(ctx, "reports.business_day_start")
			if err != nil {
				return nil, err
			}
			hh, mm := ParseBusinessDayStart(v)
			from, to := lastDays(args["days"])
			return NewPOSRepo(db).SalesByDay(ctx, from, to, hh, mm)
		},
	},
	"items.top.v1": {
		Name: "items.top.v1", Permission: "view:sales", Args: []CoreViewArg{argDays, argLimit},
		Run: func(ctx context.Context, db *sql.DB, args map[string]int) (any, error) {
			from, to := lastDays(args["days"])
			return NewPOSRepo(db).TopItems(ctx, from, to, args["limit"])
		},
	},
	"payments.breakdown.v1": {
		Name: "payments.breakdown.v1", Permission: "view:sales", Args: []CoreViewArg{argDays},
		Run: func(ctx context.Context, db *sql.DB, args map[string]int) (any, error) {
			from, to := lastDays(args["days"])
			return NewPOSRepo(db).PaymentBreakdown(ctx, from, to)
		},
	},
	"stock.levels.v1": {
		Name: "stock.levels.v1", Permission: "view:inventory",
		Run: func(ctx context.Context, db *sql.DB, _ map[string]int) (any, error) {
			return NewPOSRepo(db).ListStockLevels(ctx)
		},
	},
	"audit.summary.v1": {
		Name: "audit.summary.v1", Permission: "view:audit", Args: []CoreViewArg{argDays},
		Run: func(ctx context.Context, db *sql.DB, args map[string]int) (any, error) {
			return NewPOSRepo(db).AuditActionSummary(ctx, args["days"], auditSummaryRows)
		},
	},
}
