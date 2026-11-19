package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// renderOnlineChannelsTab is Reports → Online channels (ADR-0169 §9,
// ut-docs#5427). The caller (the /ui/reports/tab/{name} switch) has already
// applied requirePage(d,w,r,"reports"). It reads only channel_orders and
// touches no till figure. A repo error renders the same partial with a muted
// line (status 200): httpx.RenderError would swap a full layout into the panel.
func renderOnlineChannelsTab(d *common.Deps, r *http.Request, window reportWindow) http.HandlerFunc {
	sum, err := data.NewChannelOrdersRepo(d.Db).SummaryByWindow(r.Context(), window.From, window.To)
	if err != nil {
		logging.L().Errorf("[reports] online channels: %v", err)
	}
	return httpx.RenderPartial("ui/partials/reports_tab_online_channels.html", map[string]any{
		"Summary":    sum,
		"LoadFailed": err != nil,
	})
}
