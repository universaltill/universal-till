package plugins

import (
	"context"
	"log"

	"github.com/universaltill/universal-till/internal/config"
)

type Manager struct {
	// add maps, registry, loaded plugins, etc.
	MenuPlugins   map[string]MenuPlugin
	ButtonPlugins map[string]ButtonPlugin
}

type MenuPlugin struct {
	Route string
	Label string
	Name  string
	URL   string
}

type ButtonPlugin struct {
	Label  string
	Action string
}

type PopupPlugin struct {
	Label  string
	Action string
}

type PaymentPlugin struct {
	Label  string
	Action string
}

type DevicePlugin struct {
	Label  string
	Action string
}

func Init(ctx context.Context, cfg *config.Config) (*Manager, error) {
	log.Printf("initialising plugins for env=%s", cfg.Env)

	m := &Manager{
		MenuPlugins:   make(map[string]MenuPlugin),
		ButtonPlugins: make(map[string]ButtonPlugin),
	}
	// TODO: discover & load plugins (from disk, config, etc.)
	// eg: m.loadFromDir(cfg.PluginsDir)

	return m, nil
}
