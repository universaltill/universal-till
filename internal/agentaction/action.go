// Package agentaction is the core action registry behind ADR-0153
// Decision 3 (ut-docs#2838): the only changes an AI-agent plugin may ask
// for. The plugin proposes (plugins.hostActionPropose), core works out the
// before/after rows itself and keeps the proposal in memory (Store); the
// operator confirms on a core route, and only then does Apply change the
// database — the whole batch in one transaction, through the same repo
// code the manual screen uses.
//
// Never registered here (ADR-0153 Decision 3): anything on the sale or
// checkout path, fiscal/tax, payments, shifts, users/roles/PINs, plugin
// install and grants, AI settings, secrets, backups, data deletion.
package agentaction

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/pos"
)

// MaxBatch is the ADR's ceiling on changes in one proposal.
const MaxBatch = 50

// MaxArgsBytes bounds an action's raw argument JSON.
const MaxArgsBytes = 32 << 10

var (
	// ErrInvalid: arguments that fail an action's bounds (host code -4).
	ErrInvalid = errors.New("agentaction: invalid arguments")
	// ErrStale: the "before" values no longer match (refused at confirm).
	ErrStale = errors.New("agentaction: values changed since the proposal")
)

// Value is one before/after cell, formatted for the operator's locale at
// render time. Kind: "none" (—), "text", "money" (Minor) or "number".
type Value struct {
	Kind   string  `json:"kind"`
	Text   string  `json:"text,omitempty"`
	Minor  int64   `json:"minor,omitempty"`
	Number float64 `json:"number,omitempty"`
}

// Row is one change as the operator sees it: the item, the changed field
// (a core locale key), before and after.
type Row struct {
	Item     string `json:"item"`
	FieldKey string `json:"field"`
	Before   Value  `json:"before"`
	After    Value  `json:"after"`
}

// Env is what an action reads and writes through.
type Env struct {
	DB *sql.DB
}

// StockChange is one applied stock adjustment, for the caller's
// stock.adjusted publish and link nudge after commit.
type StockChange struct {
	ItemID     string
	LocationID string
	Delta      float64
}

// Result is what Apply did.
type Result struct {
	Changes int
	// CreatedItems: the ids catalog.item.create.v1 inserted.
	CreatedItems []string
	Stock        []StockChange
}

// Action is one registry entry.
type Action struct {
	Name string
	// Area is the ai.tools.write:<area> grant the plugin needs.
	Area string
	// RolePermission is the role permission the manual screen checks.
	RolePermission string
	// LabelKey is the core locale key of the action's name.
	LabelKey string
	// parse validates raw args against the action's bounds (ErrInvalid).
	parse func(raw []byte) (any, error)
	// plan works out the before/after rows; state is what apply re-checks.
	plan func(ctx context.Context, env Env, args any) ([]Row, any, error)
	// check runs before the transaction (reads through env.DB, ErrStale);
	// its result is apply's prep. nil: nothing to check outside the tx.
	check func(ctx context.Context, env Env, args, state any) (any, error)
	// apply re-checks what it can inside tx (ErrStale) and writes; it
	// reads and writes through tx only (a test DB has one connection).
	apply func(ctx context.Context, env Env, tx *sql.Tx, args, state, prep any, actorID string) (Result, error)
}

var registry = map[string]*Action{}

func register(a *Action) {
	if _, dup := registry[a.Name]; dup {
		panic("agentaction: duplicate action " + a.Name)
	}
	registry[a.Name] = a
}

// Lookup returns the named action.
func Lookup(name string) (*Action, bool) {
	a, ok := registry[name]
	return a, ok
}

// Prepare parses raw args and plans the change: (args, rows, state).
// Errors wrapping ErrInvalid are the plugin's fault.
func (a *Action) Prepare(ctx context.Context, env Env, raw []byte) (any, []Row, any, error) {
	if len(raw) == 0 || len(raw) > MaxArgsBytes {
		return nil, nil, nil, ErrInvalid
	}
	args, err := a.parse(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	rows, state, err := a.plan(ctx, env, args)
	if err != nil {
		return nil, nil, nil, err
	}
	return args, rows, state, nil
}

// Apply runs the batch in one transaction; audit (may be nil) runs in the
// same transaction before commit, so the confirmed row lands with the
// change or not at all.
func (a *Action) Apply(ctx context.Context, env Env, args, state any, actorID string, audit func(tx *sql.Tx, res Result) error) (Result, error) {
	var prep any
	if a.check != nil {
		var err error
		if prep, err = a.check(ctx, env, args, state); err != nil {
			return Result{}, err
		}
	}
	tx, err := env.DB.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("agentaction %s: begin: %w", a.Name, err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := a.apply(ctx, env, tx, args, state, prep, actorID)
	if err != nil {
		return Result{}, err
	}
	if audit != nil {
		if err := audit(tx, res); err != nil {
			return Result{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("agentaction %s: commit: %w", a.Name, err)
	}
	return res, nil
}

// decodeStrict decodes raw into v, refusing unknown fields and trailing
// data.
func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data", ErrInvalid)
	}
	return nil
}

// cleanText trims s and checks it is 1..maxRunes of printable UTF-8: no
// control characters and no invisible format characters (bidi overrides,
// zero-width), which would make the confirm card show a different name.
func cleanText(s string, maxRunes int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxRunes {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", false
		}
	}
	return s, true
}

// stockLocation is the till's own stock location; core replaces it with
// the register-aware resolver the manual screens use.
var stockLocation = func(ctx context.Context, db *sql.DB) (string, error) {
	return pos.ResolveStockLocationID(ctx, db, "")
}

// SetStockLocationResolver replaces how stock.adjust.v1 finds the till's
// stock location.
func SetStockLocationResolver(fn func(ctx context.Context, db *sql.DB) (string, error)) {
	if fn != nil {
		stockLocation = fn
	}
}

// proposeRefusal is the propose-time gate beyond grants and roles: "" lets a
// proposal be stored, else the reason it is refused. Core wires the till's
// rules (e.g. a satellite till never takes catalogue proposals).
var proposeRefusal = func(context.Context, *Action) string { return "" }

// SetProposeRefusal replaces the propose-time gate.
func SetProposeRefusal(fn func(ctx context.Context, a *Action) string) {
	if fn != nil {
		proposeRefusal = fn
	}
}

// ProposeRefusal is "" when a may be proposed (and confirmed), else why not.
func ProposeRefusal(ctx context.Context, a *Action) string { return proposeRefusal(ctx, a) }
