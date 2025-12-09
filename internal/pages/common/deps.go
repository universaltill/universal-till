package common

import (
	"database/sql"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

type Deps struct {
	Cfg         *config.Config
	Pm          *plugins.Manager
	Db          *sql.DB
	Settings    *settings.Store
	State       RuntimeState
	BaseMenu    []MenuItem
	Menu        []MenuItem
	Engine      *pos.Service
	BtnStore    *ui.ButtonStore
	CatalogRepo *marketplace.CatalogRepository
}

// RuntimeState mirrors fields needed from pages.state (theme, tax, currency).
type RuntimeState struct {
	Theme                  string
	Currency               string
	Country                string
	Region                 string
	TaxInclusive           bool
	TaxRatePct             int
	AllowNegativeInventory bool
}

type MenuItem struct {
	Href  string
	Label string
}
