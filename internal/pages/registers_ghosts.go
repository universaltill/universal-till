package pages

import (
	"context"
	"github.com/universaltill/universal-till/internal/enroll"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/registeraudit"
	"github.com/universaltill/universal-till/internal/tills"
)

// registerGhost is what the Registers page knows about one register beyond its
// name: the till it belongs to and the audit's verdict (ut-docs#7098).
type registerGhost struct {
	TillName   string
	IsSelf     bool
	StateKey   string // registers.state.* key, "" for nothing to say
	Stale      bool   // old entry or replaced: the Retire button is highlighted
	RetiredKey string // registers.retired_reason.* key for an inactive row
	RetiredOn  time.Time
	Result     registeraudit.Result
}

// mergedTillRows is the Tills page's merged list, for the register verdicts.
// fetch=false never touches the network (cached account list only); an
// unreadable account list is CloudOK=false, never "empty" (ADR-0003).
func mergedTillRows(ctx context.Context, d *common.Deps, fetch bool) ([]tills.Row, bool) {
	var devs []enroll.Device
	var ok bool
	if fetch {
		devs, ok = accountFleet(ctx, d, false)
	} else {
		devs, ok = cachedAccountFleet()
	}
	if !ok {
		return nil, false
	}
	list, err := data.NewTillsRepo(d.Db).ListTills(ctx)
	if err != nil {
		return nil, false
	}
	selfID, _ := discovery.ReportedTillID(ctx, d.Settings)
	self := tills.SelfInfo{ID: selfID, Name: deviceNameOrDefault(ctx, d, httpx.DefaultLocale()), Role: tills.RoleMain}
	return tills.Merge(tills.Source{Cloud: devs, CloudOK: true, Local: list, Self: self}, time.Now(), tillsStaleAfter), true
}

// registerVerdicts classifies every register with the same function the
// register-audit tool uses. rows may be nil (account list unknown): then no
// register is treated as gone because of the account, only because its till
// row is missing here.
func registerVerdicts(ctx context.Context, d *common.Deps, rows []tills.Row, now time.Time, quietDays int) (map[string]registeraudit.Result, map[string]data.RegisterAuditRow, error) {
	regs, err := data.AuditRegisters(ctx, d.Db)
	if err != nil {
		return nil, nil, err
	}
	own := data.AuditOwnRegisterID(ctx, d.Db)
	goneTill := map[string]bool{}
	for _, r := range rows {
		if r.State == tills.StateOldEntry && !r.IsSelf {
			goneTill[r.ID] = true
		}
	}
	active := 0
	for i := range regs {
		regs[i].IsSelf = regs[i].RegisterID == own
		if regs[i].IsActive {
			active++
		}
	}
	verdicts := make(map[string]registeraudit.Result, len(regs))
	byID := make(map[string]data.RegisterAuditRow, len(regs))
	for _, r := range regs {
		verdicts[r.RegisterID] = registeraudit.Classify(r, registeraudit.Context{
			Now: now, QuietDays: quietDays, ActiveCount: active, TillGone: goneTill[r.TillID],
		})
		byID[r.RegisterID] = r
	}
	return verdicts, byID, nil
}

// registerGhostFor turns a verdict into what the row shows.
func registerGhostFor(res registeraudit.Result, row data.RegisterAuditRow) registerGhost {
	g := registerGhost{TillName: row.TillName, IsSelf: row.IsSelf, Result: res, RetiredOn: row.RetiredAt}
	switch res.State {
	case registeraudit.StateLive:
		g.StateKey = "registers.state.live"
	case registeraudit.StateOldEntry:
		g.StateKey, g.Stale = "registers.state.old_entry", true
	case registeraudit.StateSuperseded:
		g.StateKey, g.Stale = "registers.state.superseded", true
	case registeraudit.StateUnknown:
		g.StateKey = "registers.state.unknown"
	case registeraudit.StateRetired:
		switch row.RetiredReason {
		case data.RetiredRevoked, data.RetiredOldEntry, data.RetiredStaleAuto, data.RetiredManual, data.RetiredReplaced:
			g.RetiredKey = "registers.retired_reason." + row.RetiredReason
		}
	}
	return g
}

// activeTotal counts the active registers among the verdicts (every verdict
// that is not "retired").
func activeTotal(v map[string]registeraudit.Result) int {
	n := 0
	for _, r := range v {
		if r.State != registeraudit.StateRetired {
			n++
		}
	}
	return n
}
