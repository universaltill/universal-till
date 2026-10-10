// Package bgremove is the till's host-side background removal for item and
// category tile photos — the "image" capability (ut-docs#3126, docs repo
// architecture/ai-product-photo.md §4). It stays in core because it
// post-processes photos the host stores; the AI text engine (camera
// identify, "Ask your till") lives in the AI Assistant plugin
// (ut-plugin-integration-ai, ut-docs#2851).
//
// It has its own provider/endpoint/model: an image-segmentation server is a
// different capability from a text/vision model. This is the first
// per-capability slot of ADR-0126 §6, ahead of the generalisation in
// ut-docs#3108 (same <capability>_provider|_endpoint|_model key shape).
// Offline-first (ADR-0003): nothing here sits on the sale path; callers
// treat every error as "feature unavailable" and keep the original photo.
package bgremove

import (
	"context"
	"errors"
	"image"
	"os"
	"strings"

	"github.com/universaltill/universal-till/internal/plugins"
)

// ImageConfig configures background removal. An empty Endpoint means off.
type ImageConfig struct {
	Provider string // only "self_hosted" (a rembg server) today; anything else is off
	Endpoint string // rembg base URL, e.g. http://192.168.1.20:7000
	Model    string // must pass ImageModelAllowed; empty means DefaultImageModel
}

// ImageProviderSelfHosted is the only image provider this build implements:
// the shop's own rembg server (`rembg s`, github.com/danielgatis/rembg, MIT).
const ImageProviderSelfHosted = "self_hosted"

// DefaultImageModel is the rembg model used when none is configured.
const DefaultImageModel = "birefnet-general-lite"

// allowedImageModels is an ALLOW-list of rembg models whose weights carry a
// licence that permits commercial use, each checked at its upstream source
// (2026-10-09). rembg's own default, bria-rmbg, needs a paid agreement for
// commercial use, so it — and any bria-* or future model nobody checked —
// turns the capability off rather than running by accident.
var allowedImageModels = []string{
	// BiRefNet — MIT License:
	// https://github.com/ZhengPeng7/BiRefNet/blob/main/LICENSE
	"birefnet-general-lite",
	// U²-Net (small variant) — Apache License 2.0:
	// https://github.com/xuebinqin/U-2-Net/blob/master/LICENSE
	"u2netp",
}

// ImageModelAllowed reports whether model is on the licence-checked
// allow-list (exact match; the empty string is not a model — resolution
// maps it to DefaultImageModel before asking).
func ImageModelAllowed(model string) bool {
	for _, m := range allowedImageModels {
		if model == m {
			return true
		}
	}
	return false
}

// cutter is the background-removal capability. It returns an image with an
// alpha channel (transparent where the background was).
type cutter interface {
	removeBackground(ctx context.Context, photo []byte, mediaType string) (image.Image, error)
}

// newCutter builds the image capability from its config, or nil (off).
// Fail-safe (ADR-0126 §7): only the exact provider "self_hosted" builds an
// adapter; any other value — a typo, a hosted vendor name this build has no
// adapter for — is off and never makes a call.
func newCutter(ic ImageConfig) cutter {
	if ic.Provider != ImageProviderSelfHosted || !validImageEndpoint(ic.Endpoint) {
		return nil
	}
	model := ic.Model
	if model == "" {
		model = DefaultImageModel
	}
	if !ImageModelAllowed(model) {
		return nil
	}
	return newRembgCutter(ic.Endpoint, model)
}

// validImageEndpoint accepts only a plain http(s)://host[:port][/path] URL
// — the endpoint is shop-entered (plugin setting or env). The same rule the
// settings page enforces when an operator saves a `type: "endpoint"`
// setting (ADR-0121 §2), applied here too so an env value or a row saved by
// an older till can't widen it.
func validImageEndpoint(endpoint string) bool {
	return plugins.ValidEndpointURL(endpoint)
}

// Service is the till-side background-removal facade. A nil Service is safe
// to call CanCutout() on, so pages can decide whether to render the
// affordance.
type Service struct {
	cut cutter // nil = off
}

// New builds the capability from its config; an unusable config yields a
// Service whose CanCutout() is false.
func New(ic ImageConfig) *Service { return &Service{cut: newCutter(ic)} }

// FromEnv reads UT_AI_IMAGE_PROVIDER / UT_AI_IMAGE_ENDPOINT /
// UT_AI_IMAGE_MODEL: an endpoint with no provider means self_hosted (rembg),
// an empty model means DefaultImageModel, no endpoint means off.
func FromEnv() ImageConfig {
	ic := ImageConfig{
		Provider: strings.ToLower(strings.TrimSpace(os.Getenv("UT_AI_IMAGE_PROVIDER"))),
		Endpoint: strings.TrimSpace(os.Getenv("UT_AI_IMAGE_ENDPOINT")),
		Model:    strings.TrimSpace(os.Getenv("UT_AI_IMAGE_MODEL")),
	}
	if ic.Endpoint == "" {
		return ImageConfig{}
	}
	if ic.Provider == "" {
		ic.Provider = ImageProviderSelfHosted
	}
	if ic.Model == "" {
		ic.Model = DefaultImageModel
	}
	return ic
}

// CanCutout reports whether background removal is configured.
func (s *Service) CanCutout() bool { return s != nil && s.cut != nil }

// RemoveBackground returns the photo with its background made transparent.
// Callers treat any error as "feature unavailable" and keep the original
// photo (ADR-0003: nothing here is on the sale path).
func (s *Service) RemoveBackground(ctx context.Context, photo []byte, mediaType string) (image.Image, error) {
	if !s.CanCutout() {
		return nil, errors.New("background removal not configured")
	}
	if len(photo) == 0 {
		return nil, errors.New("photo required")
	}
	return s.cut.removeBackground(ctx, photo, mediaType)
}
