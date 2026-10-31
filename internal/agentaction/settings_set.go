package agentaction

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// settings.set.v1: change allow-listed shop settings {changes:[{key,
// value}]} (ADR-0153 Decision 3, ut-docs#4129). The allow-list is the
// writable entries of data.AgentSettings; every other key (fiscal, tax,
// payments, secrets, users, PINs, roles, plugin install, update channel,
// cloud pairing, AI settings, and every explain-only screen setting) is
// refused at parse time. Before/after come from the stored value; at
// confirm the value is re-read inside the transaction (any difference is
// stale). The write is the settings row the manual screen writes, with the
// manual screen's audit event (settingsAudit: entity "settings", the
// entry's audit action, {enabled: bool}) in the same transaction.

const settingsBatchMax = 10

type settingChange struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type settingsSetArgs struct {
	Changes []settingChange `json:"changes"`
}

type settingsSetState struct {
	// Before holds each key's on/off state at propose time.
	Before []bool
}

func init() {
	register(&Action{
		Name:           "settings.set.v1",
		Area:           "settings",
		RolePermission: "settings",
		LabelKey:       "agent.action.settings_set",
		parse:          parseSettingsSet,
		plan:           planSettingsSet,
		apply:          applySettingsSet,
	})
}

func settingOn(v string) (on, ok bool) {
	switch v {
	case "on":
		return true, true
	case "off":
		return false, true
	}
	return false, false
}

func parseSettingsSet(raw []byte) (any, error) {
	var a settingsSetArgs
	if err := decodeStrict(raw, &a); err != nil {
		return nil, err
	}
	if len(a.Changes) == 0 || len(a.Changes) > settingsBatchMax {
		return nil, fmt.Errorf("%w: 1..%d changes", ErrInvalid, settingsBatchMax)
	}
	seen := map[string]bool{}
	for i, c := range a.Changes {
		s, ok := data.LookupAgentSetting(c.Key)
		if !ok || !s.Writable || s.Kind != data.AgentSettingBool || s.Audit == "" || seen[c.Key] {
			return nil, fmt.Errorf("%w: changes[%d].key", ErrInvalid, i)
		}
		seen[c.Key] = true
		if _, ok := settingOn(c.Value); !ok {
			return nil, fmt.Errorf("%w: changes[%d].value must be on or off", ErrInvalid, i)
		}
	}
	return a, nil
}

func onOffValue(on bool) Value {
	if on {
		return Value{Kind: "key", Text: "agent.value.on"}
	}
	return Value{Kind: "key", Text: "agent.value.off"}
}

func currentBool(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, s data.AgentSetting) (bool, error) {
	raw, found, err := data.AgentSettingRaw(ctx, q, s.Key)
	if err != nil {
		return false, err
	}
	return s.BoolState(raw, found), nil
}

func planSettingsSet(ctx context.Context, env Env, args any) ([]Row, any, error) {
	a := args.(settingsSetArgs)
	var st settingsSetState
	rows := make([]Row, 0, len(a.Changes))
	for i, c := range a.Changes {
		s, _ := data.LookupAgentSetting(c.Key)
		before, err := currentBool(ctx, env.DB, s)
		if err != nil {
			return nil, nil, err
		}
		after, _ := settingOn(c.Value)
		if before == after {
			return nil, nil, fmt.Errorf("%w: changes[%d] is already %s", ErrInvalid, i, c.Value)
		}
		st.Before = append(st.Before, before)
		rows = append(rows, Row{ItemKey: s.LabelKey, FieldKey: "agent.field.setting",
			Before: onOffValue(before), After: onOffValue(after)})
	}
	return rows, st, nil
}

func applySettingsSet(ctx context.Context, env Env, tx *sql.Tx, args, state, _ any, actorID string) (Result, error) {
	a := args.(settingsSetArgs)
	st := state.(settingsSetState)
	var res Result
	repo := data.NewPOSRepo(env.DB)
	now := time.Now().UTC().Format(time.RFC3339)
	for i, c := range a.Changes {
		s, _ := data.LookupAgentSetting(c.Key)
		cur, err := currentBool(ctx, tx, s)
		if err != nil {
			return Result{}, err
		}
		if cur != st.Before[i] {
			return Result{}, ErrStale
		}
		on, _ := settingOn(c.Value)
		val := s.Stored(on)
		if err := data.NewSettingsRepo(env.DB).SetTx(ctx, tx, s.Key, val); err != nil {
			return Result{}, err
		}
		if err := repo.InsertAudit(ctx, tx, actorID, "settings", s.Key, s.Audit, map[string]any{"enabled": on}, now, ""); err != nil {
			return Result{}, fmt.Errorf("settings audit: %w", err)
		}
		res.Settings = append(res.Settings, SettingChange{Key: s.Key, Value: val})
		res.Changes++
	}
	return res, nil
}
