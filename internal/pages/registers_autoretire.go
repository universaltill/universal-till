package pages

import (
	"context"
	"strconv"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/registeraudit"
	"github.com/universaltill/universal-till/internal/tills"
)

// Optional auto-retire of old registers (ut-docs#7098, priority 4). DEFAULT
// OFF: nothing is retired unless a manager ticked the box on the Registers
// page. Kept in its own file so it can be reverted alone.
//
// There is no scheduler (that would touch init.go): the sweep runs lazily from
// the two pages that already hold the merged Tills list, on the main till
// only, at most once every 24 h, and only while the account list could be
// read (an unknown list never retires anything, ADR-0003). It retires only
// what Classify calls AutoRetireOK: its till is gone or replaced, the device
// id is known, no sale for the quiet period, no open shift, no unsent sale,
// never the shop's own or the last active register. Reversible (Activate).
const (
	settingAutoRetireEnabled = "registers.auto_retire_enabled" // "1" = on, absent/0 = off
	settingAutoRetireDays    = "registers.auto_retire_days"    // 7..365, default 30
	settingAutoRetireLastRun = "registers.auto_retire_last_run"
	settingAutoRetireNotice  = "registers.retired_auto_notice" // count not yet shown

	autoRetireEvery = 24 * time.Hour
)

func autoRetireSettings(ctx context.Context, d *common.Deps) bool {
	v, ok, err := d.Settings.Get(ctx, settingAutoRetireEnabled)
	return err == nil && ok && v == "1"
}

// autoRetireDays is the quiet period, clamped to 7..365 (default 30).
func autoRetireDays(ctx context.Context, d *common.Deps) int {
	days := registeraudit.DefaultQuietDays
	if v, ok, err := d.Settings.Get(ctx, settingAutoRetireDays); err == nil && ok {
		if n, perr := strconv.Atoi(v); perr == nil {
			days = n
		}
	}
	if days < 7 {
		days = 7
	}
	if days > 365 {
		days = 365
	}
	return days
}

// takeAutoRetireNotice returns how many registers were retired automatically
// since the page last showed it, and clears the count (shown once).
func takeAutoRetireNotice(ctx context.Context, d *common.Deps) int {
	v, ok, err := d.Settings.Get(ctx, settingAutoRetireNotice)
	if err != nil || !ok {
		return 0
	}
	n, _ := strconv.Atoi(v)
	if n > 0 {
		_ = d.Settings.Set(ctx, settingAutoRetireNotice, "0")
	}
	return n
}

// maybeAutoRetireRegisters runs the sweep when it is due. rows/cloudOK are the
// merged Tills list the caller already built. It never returns an error: a
// failed sweep is logged and tried again on the next visit.
func maybeAutoRetireRegisters(ctx context.Context, d *common.Deps, rows []tills.Row, cloudOK bool) int {
	if !autoRetireSettings(ctx, d) || !cloudOK || d.SyncPrimaryURL(ctx) != "" {
		return 0
	}
	now := time.Now()
	if v, ok, err := d.Settings.Get(ctx, settingAutoRetireLastRun); err == nil && ok {
		if last, perr := time.Parse(time.RFC3339, v); perr == nil && now.Sub(last) < autoRetireEvery {
			return 0
		}
	}
	verdicts, _, err := registerVerdicts(ctx, d, rows, now, autoRetireDays(ctx, d))
	if err != nil {
		logging.L().Warnf("registers: auto-retire sweep: %v", err)
		return 0
	}
	repo := data.NewPOSRepo(d.Db)
	var retired []string
	for id, res := range verdicts {
		if !res.AutoRetireOK {
			continue
		}
		reason := data.RetiredStaleAuto
		if _, rerr := repo.RetireRegister(ctx, id, reason, "", data.RetireOpts{}); rerr != nil {
			continue // a guard bit between the look and the write: leave it
		}
		retired = append(retired, id)
	}
	_ = d.Settings.Set(ctx, settingAutoRetireLastRun, now.UTC().Format(time.RFC3339))
	if len(retired) > 0 {
		_ = repo.InsertAudit(ctx, nil, "system", "register", "auto_retire", "register_auto_retire_run",
			map[string]any{"retired": retired, "days": autoRetireDays(ctx, d)}, now.UTC().Format(time.RFC3339), "")
		prev := 0
		if v, ok, gerr := d.Settings.Get(ctx, settingAutoRetireNotice); gerr == nil && ok {
			prev, _ = strconv.Atoi(v)
		}
		_ = d.Settings.Set(ctx, settingAutoRetireNotice, strconv.Itoa(prev+len(retired)))
	}
	return len(retired)
}
