package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// tips.suggest.ask (ut-docs#7108, ADR-0149 §6): the payout form asks the
// plugins the operator granted payout:suggest for per-person amounts for a
// date range. The answer only SUGGESTS: core keeps rows whose user_id is a
// real till user and whose amount is positive, shows them, and the manager
// confirms or edits each one through the existing record-a-payout form. The
// plugin never writes worker_allocations (ADR-0063; the logbook is
// attribution, not the legal tip record).
const (
	tipsSuggestEvent      = "tips.suggest.ask"
	tipsSuggestPermission = "payout:suggest"
	tipsSuggestTimeout    = 5 * time.Second
	// maxTipsSuggestRows bounds what one answer can put on screen.
	maxTipsSuggestRows = 200
	// maxTipsSuggestLabelRunes caps an unmapped label (plugin text on a
	// manager's screen) after control/format characters are stripped.
	maxTipsSuggestLabelRunes = 80
	// maxTipsSuggestAmountMinor is the sanity ceiling for one suggested
	// amount (the same 1,000,000.00 ceiling as pos.MaxVoucherIssueAmount):
	// anything above it is a plugin bug, never a prefill.
	maxTipsSuggestAmountMinor = 100_000_000
)

type tipsSuggestPayload struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type tipsSuggestAnswer struct {
	Suggestions []struct {
		UserID      string `json:"user_id"`
		AmountMinor int64  `json:"amount_minor"`
	} `json:"suggestions"`
	Unmapped []struct {
		Label       string `json:"label"`
		AmountMinor int64  `json:"amount_minor"`
	} `json:"unmapped"`
}

type tipsSuggestRow struct {
	CashierID   string
	Name        string
	AmountMinor int64
	Plain       string // the amount as the form's text field reads it
}

type tipsUnmappedRow struct {
	Label       string
	AmountMinor int64
}

// registerTipsSuggestAPI mounts GET /api/reports/tips-suggestions behind the
// same `worker_allocation` permission as the payout write.
func registerTipsSuggestAPI(mux *http.ServeMux, d *common.Deps) {
	authRepo := data.NewAuthRepo(d.Db)
	mux.HandleFunc("GET /api/reports/tips-suggestions", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "worker_allocation") {
			http.Error(w, "worker_allocation permission required", http.StatusForbidden)
			return
		}
		from := strings.TrimSpace(r.URL.Query().Get("from"))
		to := strings.TrimSpace(r.URL.Query().Get("to"))
		if !validTipsDate(from) || !validTipsDate(to) || from > to {
			http.Error(w, "from and to must be YYYY-MM-DD dates, from not after to", http.StatusBadRequest)
			return
		}

		var ans tipsSuggestAnswer
		ctx, cancel := context.WithTimeout(r.Context(), tipsSuggestTimeout)
		defer cancel()
		resp, ok, err := plugins.SharedBus(d.Db).AskPermitted(ctx, tipsSuggestEvent, tipsSuggestPermission,
			tipsSuggestPayload{From: from, To: to})
		// A plugin that errors or answers garbage means "no suggestion": the
		// manager types the amounts as before. Never an error screen.
		if err == nil && ok {
			if json.Unmarshal(resp, &ans) != nil {
				ans = tipsSuggestAnswer{}
			}
		}

		cur := httpx.ActiveCurrency()
		users, _ := authRepo.ListUsers(ctx)
		names := workerAllocationDisplayNames(users)
		var rows []tipsSuggestRow
		seen := map[string]bool{}
		for _, s := range ans.Suggestions {
			name, real := names[s.UserID]
			if !real || s.AmountMinor <= 0 || s.AmountMinor > maxTipsSuggestAmountMinor || seen[s.UserID] || len(rows) >= maxTipsSuggestRows {
				continue
			}
			seen[s.UserID] = true
			if name == "" {
				name = s.UserID
			}
			rows = append(rows, tipsSuggestRow{CashierID: s.UserID, Name: name, AmountMinor: s.AmountMinor,
				Plain: httpx.FormatMajorPlain(s.AmountMinor, cur.Decimals)})
		}
		var unmapped []tipsUnmappedRow
		for _, u := range ans.Unmapped {
			label := tipsSuggestLabel(u.Label)
			if u.AmountMinor > 0 && u.AmountMinor <= maxTipsSuggestAmountMinor && label != "" && len(unmapped) < maxTipsSuggestRows {
				unmapped = append(unmapped, tipsUnmappedRow{Label: label, AmountMinor: u.AmountMinor})
			}
		}
		httpx.RenderPartial("ui/partials/reports_tips_suggest.html", map[string]any{
			"Rows": rows, "Unmapped": unmapped, "From": from, "To": to,
		})(w, r)
	})
}

// tipsSuggestLabel drops control and format characters (incl. bidi
// overrides) from a plugin's unmapped label, trims it and cuts it to
// maxTipsSuggestLabelRunes runes.
func tipsSuggestLabel(raw string) string {
	var b strings.Builder
	n := 0
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			continue
		}
		if n == maxTipsSuggestLabelRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

func validTipsDate(s string) bool {
	if !eodDateRe.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}
