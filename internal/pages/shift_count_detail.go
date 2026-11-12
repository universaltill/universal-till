package pages

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

type shiftCountEntryDTO struct {
	DenominationMinor int64 `json:"denomination_minor"`
	Count             int64 `json:"count"`
	LineTotalMinor    int64 `json:"line_total_minor"`
}

type shiftCountDTO struct {
	ShiftID      string               `json:"shift_id"`
	OpenedAt     string               `json:"opened_at"`
	ClosedAt     string               `json:"closed_at"`
	Currency     string               `json:"currency"`
	Recorded     bool                 `json:"recorded"`
	Readable     bool                 `json:"readable"`
	Partial      bool                 `json:"partial"`
	Entries      []shiftCountEntryDTO `json:"entries"`
	TotalMinor   int64                `json:"total_minor"`
	CountedMinor int64                `json:"counted_minor"`
}

// registerShiftCountDetail wires GET /ui/shifts/{id}/count (ut-docs#1043): a
// closed shift's stored denomination count, as an HTML fragment for the
// /shifts history panel or JSON on Accept: application/json. Read-only: no
// audit row, no sync, nothing written. Not PIN-gated (design D1): /shifts is
// itself ungated and the page already shows every closed shift's Counted
// figure; this is only its breakdown.
func registerShiftCountDetail(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/shifts/{id}/count", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if len(id) > xShiftIDMaxLen {
			writeXReportError(w, r, http.StatusBadRequest, "invalid_request", "shifts.count_detail.invalid_request", nil)
			return
		}
		if id == "" {
			writeXReportError(w, r, http.StatusNotFound, "shift_not_closed", "shifts.count_detail.not_closed", nil)
			return
		}
		sc, err := data.NewPOSRepo(d.Db).ShiftCount(r.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, data.ErrShiftNotClosed) {
				writeXReportError(w, r, http.StatusNotFound, "shift_not_closed", "shifts.count_detail.not_closed", nil)
				return
			}
			writeXReportError(w, r, http.StatusInternalServerError, "count_detail_failed", "shifts.count_detail.failed", err)
			return
		}
		rd := pos.ReadCountProtocol(sc.Protocol)
		if wantsJSON(r) {
			dto := shiftCountDTO{
				ShiftID: sc.ShiftID, OpenedAt: xISOTime(sc.OpenedAt), ClosedAt: xISOTime(sc.ClosedAt),
				Currency: httpx.ActiveCurrency().Code, Recorded: rd.Recorded, Readable: !rd.Unreadable,
				Partial: rd.Partial, Entries: make([]shiftCountEntryDTO, 0, len(rd.Lines)),
				TotalMinor: rd.Total.Minor(), CountedMinor: sc.Counted.Minor(),
			}
			for _, l := range rd.Lines {
				dto.Entries = append(dto.Entries, shiftCountEntryDTO{l.Denomination.Minor(), l.Pieces, l.Total.Minor()})
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": dto, "error": nil})
			return
		}
		// A partial read leaves the dropped entries out of rd.Total, so a
		// comparison with Counted would be meaningless (review of #1043).
		mismatch := rd.Recorded && !rd.Unreadable && !rd.Partial && rd.Total != sc.Counted
		httpx.RenderPartial("ui/partials/shift_count_detail.html", map[string]any{"C": sc, "R": rd, "Mismatch": mismatch})(w, r)
	})
}
