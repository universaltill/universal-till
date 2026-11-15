package pages

import (
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// settings.describe.v1 (ut-docs#4129) explains each Settings region with the
// screen's own words. The view has no request, so it returns English; the
// assistant answers in the operator's language. data cannot import httpx,
// so the translator is installed here.
func init() {
	data.SetAgentSettingsText(func(key string) string { return httpx.T("en", key) })
}
