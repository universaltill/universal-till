package pages

import (
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6455 K1b: the pending / check-again / manager-resolve surface for a
// card-present (Tap to Pay) payment. Skeleton so the failing tests compile.

// tapCheckInterval is the least time between two "Check again" asks that reach
// the broker for one attempt.
var tapCheckInterval = 2 * time.Second

func registerTapPay(mux *http.ServeMux, d *common.Deps) {}
