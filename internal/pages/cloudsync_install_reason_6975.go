package pages

import (
	"context"
	"errors"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// cloudInstallPluginDirective is the install_plugin hook (ut-docs#6975): the
// same install as cloudInstallPlugin, but a failure comes back as a
// cloudsync.ReasonError, so the result posted to the cloud carries the
// en.json message key (translated by my.) and a plain-English fallback —
// never the raw error text, which can hold file paths or URLs.
func cloudInstallPluginDirective(ctx context.Context, d *common.Deps, listingID string) (string, error) {
	msg, err := cloudInstallPlugin(ctx, d, listingID)
	if err == nil {
		return msg, nil
	}
	var already *cloudsync.ReasonError
	if errors.As(err, &already) {
		return msg, err
	}
	failure := plugins.ClassifyInstallError(err, storeHasOwner(ctx, d.Settings))
	return msg, &cloudsync.ReasonError{Key: failure.MessageKey, Message: failure.Message, Cause: err}
}
