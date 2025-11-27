package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/plugins"
)

type IPage interface {
	handle(cfg *config.Config, pm *plugins.Manager) http.HandlerFunc
}
