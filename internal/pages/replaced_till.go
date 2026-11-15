package pages

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// A till the shop's owner replaced as main till (ut-docs#5058; design §3.3
// T2; ADR-0117 amendment 2026-10-26). enroll.ReplacedState is set from the
// cloud's 401 device_revoked {reason: replaced}. From then on this till never
// acts as the main till on the shop network, whatever its local role says:
// it does not advertise (discovery.GateOnReplaced), enrol, link or take
// another till's journal. Those tills keep their sales queued; re-joining the
// new main till sends them on. Selling here stays possible (decision D4,
// ADR-0116 D6) with a chip and a banner.

// lanErrTillReplaced is the LAN error code a replaced main till answers its
// sync routes with (409), so a joining or linked till can say why.
const lanErrTillReplaced = "till_replaced"

// tillReplaced reports this till's persisted replaced state.
func tillReplaced(r *http.Request, d *common.Deps) (enroll.Replaced, bool) {
	if d == nil || d.Settings == nil {
		return enroll.Replaced{}, false
	}
	return enroll.ReplacedState(r.Context(), d.Settings)
}

// refuseIfReplaced answers a LAN sync request with 409 till_replaced when
// this till was replaced, and reports whether it did.
func refuseIfReplaced(w http.ResponseWriter, r *http.Request, d *common.Deps) bool {
	if _, replaced := tillReplaced(r, d); !replaced {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": lanErrTillReplaced})
	return true
}

// answerIsTillReplaced reports whether a main till's answer is the 409
// till_replaced refusal. It reads at most a small prefix of the body, so the
// caller must not need the body afterwards.
func answerIsTillReplaced(resp *http.Response) bool {
	if resp == nil || resp.StatusCode != http.StatusConflict {
		return false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var env struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(b, &env) != nil {
		return false
	}
	s, _ := env.Error.(string)
	return strings.TrimSpace(s) == lanErrTillReplaced
}

// replacedByText is the successor's name for the chip and banner, in a bidi
// isolate (a Latin name in an RTL sentence), or "the new main till" while the
// cloud has none.
func replacedByText(locale string, rp enroll.Replaced) string {
	if strings.TrimSpace(rp.By) == "" {
		return httpx.T(locale, "status.replaced.unnamed")
	}
	return "⁨" + rp.By + "⁩"
}

// replacedHintText is the chip's title: when, and what to use instead.
func replacedHintText(locale string, rp enroll.Replaced) string {
	when := "-"
	if t, err := time.Parse(time.RFC3339, rp.At); err == nil {
		when = "⁨" + t.UTC().Format("2006-01-02") + "⁩"
	}
	return fmt.Sprintf(httpx.T(locale, "status.cloud_auth.replaced_hint"), when, replacedByText(locale, rp))
}

// registerReplacedBanner: GET /ui/replaced-banner (ut-docs#5058, design
// §4.4), polled from base.html on every page, the sale screen included.
// Empty unless this till was replaced; then a banner that cannot be
// dismissed — never a modal, selling carries on (decision D4).
func registerReplacedBanner(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/replaced-banner", func(w http.ResponseWriter, r *http.Request) {
		rp, replaced := tillReplaced(r, d)
		if !replaced {
			w.WriteHeader(http.StatusOK)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		httpx.RenderPartial("ui/partials/replaced_banner.html", map[string]any{
			"text": fmt.Sprintf(httpx.T(locale, "status.replaced_banner"), replacedByText(locale, rp)),
		})(w, r)
	})
}
