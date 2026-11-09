package pages

import (
	"errors"
	"fmt"
	"time"

	"context"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// boundedAskPlugin asks exactly pluginID on the shared bus, bounded by
// timeout even if the handler ignores ctx; a panicking handler is an error.
// answered=false: nobody answered (no hook, events:receive refused, data
// class withheld, declined/empty). No permission check of its own: AskPlugin
// gates on events:receive; a caller wanting a page permission checks it
// first (askPluginUIAs).
func boundedAskPlugin(ctx context.Context, d *common.Deps, pluginID, event string, payload any, timeout time.Duration) (raw []byte, answered bool, err error) {
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type result struct {
		raw []byte
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer logging.RecoverAndLog("pages.pluginViewAsk")
		// Sent by a deferred call so a panicking handler still answers at
		// once (it runs before RecoverAndLog recovers).
		res := result{err: errors.New("plugin ask panicked")}
		defer func() { done <- res }()
		raw, ok, err := plugins.SharedBus(d.Db).AskPlugin(actx, pluginID, event, payload)
		res = result{raw, ok, err}
	}()
	select {
	case res := <-done:
		return res.raw, res.ok, res.err
	case <-actx.Done():
		return nil, false, fmt.Errorf("%s: %w", event, actx.Err())
	}
}
