package common

import (
	"database/sql"
	"sync"

	"github.com/universaltill/universal-till/internal/ai"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

// StateMu guards Deps.State: settings handlers replace fields while every
// request renders from them. Readers use CurrentState(), writers UpdateState.
type Deps struct {
	StateMu sync.RWMutex

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
	AuthSvc     *auth.Service
	AI          *ai.Service
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
	UIScale                float64 // interface scale for this till's screen (0 = unset)
}

// CurrentState returns a consistent copy of the runtime state for rendering.
func (d *Deps) CurrentState() RuntimeState {
	d.StateMu.RLock()
	defer d.StateMu.RUnlock()
	return d.State
}

// UpdateState applies fn to the state under the write lock and returns the
// resulting copy (for persisting via SaveState).
func (d *Deps) UpdateState(fn func(*RuntimeState)) RuntimeState {
	d.StateMu.Lock()
	defer d.StateMu.Unlock()
	fn(&d.State)
	return d.State
}

type MenuItem struct {
	Href  string
	Label string
}
