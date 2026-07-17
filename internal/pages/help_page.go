package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerHelp(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/help", func(w http.ResponseWriter, r *http.Request) {
		renderHelpPage(w, r, d)
	})
}

// helpFeatureIDs orders the expandable feature guide on the Help page. Each id
// has locale keys help.feat.<id>.title / .what plus the steps listed in
// helpFeatureSteps — add all of them to every file under web/locales/ when
// adding a feature here.
var helpFeatureIDs = []string{
	"sell", "catalog", "inventory", "reports", "invoices", "printing",
	"designer", "payments", "multitill", "plugins", "claim", "users",
	"backups", "updates", "display",
}

// helpFeatureSteps maps each feature to its usage-step locale keys.
var helpFeatureSteps = map[string][]string{
	"sell":      {"help.feat.sell.s1", "help.feat.sell.s2", "help.feat.sell.s3"},
	"catalog":   {"help.feat.catalog.s1", "help.feat.catalog.s2", "help.feat.catalog.s3"},
	"inventory": {"help.feat.inventory.s1", "help.feat.inventory.s2", "help.feat.inventory.s3"},
	"reports":   {"help.feat.reports.s1", "help.feat.reports.s2"},
	"invoices":  {"help.feat.invoices.s1", "help.feat.invoices.s2"},
	"printing":  {"help.feat.printing.s1", "help.feat.printing.s2", "help.feat.printing.s3"},
	"designer":  {"help.feat.designer.s1", "help.feat.designer.s2"},
	"payments":  {"help.feat.payments.s1", "help.feat.payments.s2", "help.feat.payments.s3"},
	"multitill": {"help.feat.multitill.s1", "help.feat.multitill.s2", "help.feat.multitill.s3"},
	"plugins":   {"help.feat.plugins.s1", "help.feat.plugins.s2", "help.feat.plugins.s3"},
	"claim":     {"help.feat.claim.s1", "help.feat.claim.s2", "help.feat.claim.s3"},
	"users":     {"help.feat.users.s1", "help.feat.users.s2", "help.feat.users.s3"},
	"backups":   {"help.feat.backups.s1", "help.feat.backups.s2"},
	"updates":   {"help.feat.updates.s1", "help.feat.updates.s2"},
	"display":   {"help.feat.display.s1", "help.feat.display.s2", "help.feat.display.s3"},
}

func renderHelpPage(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	data := map[string]any{
		"title":        "Help",
		"theme":        d.CurrentState().Theme,
		"menuItems":    d.Menu,
		"featureIDs":   helpFeatureIDs,
		"featureSteps": helpFeatureSteps,
	}
	httpx.Render("ui/pages/help.html", data)(w, r)
}
