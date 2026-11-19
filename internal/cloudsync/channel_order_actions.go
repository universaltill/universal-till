package cloudsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/channelactions"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

// Channel order action upload scheduling (ADR-0169 §9, ut-docs#4768 slice D).
// internal/channelactions does the upload; this file decides WHEN and WHO:
//
//   - who: only the main till (no sync.primary_url) holding a complete cloud
//     enrolment, and only when a channel_order hook exists. The cloud checks
//     the device id too (403 main_till_required), so this is defence in depth.
//   - when: in the tick, straight after the check-in answered and BEFORE that
//     tick's channel_order(_update) directives are applied (channel-order-
//     action.md "Upload order"), so an action is judged against the cloud's
//     newest state and its answer's `current` is applied first; a directive of
//     the same revision is then ignored. A handler also kicks a check-in after
//     it queues an action (Deps.CloudSyncNow), so an online till does not wait
//     for the next tick; kicks are ignored during the loop's own backoff.
//
// Nothing here is on the sale path: a tap only writes the outbox row. Failures
// are logged, never returned: the rows stay pending and the next tick retries.

var channelActionRunTimeout = 60 * time.Second

const (
	channelActionBackoffStart = 30 * time.Second
	channelActionBackoffMax   = 5 * time.Minute
)

var (
	channelActionMu        sync.Mutex
	channelActionHoldUntil time.Time
	channelActionBackoff   time.Duration
	// channelActionWarned is set once a transient failure has been logged at
	// Warn; reset by the next successful run.
	channelActionWarned atomic.Bool
)

func channelActionFailed(now time.Time) {
	channelActionMu.Lock()
	defer channelActionMu.Unlock()
	if channelActionBackoff == 0 {
		channelActionBackoff = channelActionBackoffStart
	} else {
		channelActionBackoff = min(channelActionBackoff*2, channelActionBackoffMax)
	}
	channelActionHoldUntil = now.Add(channelActionBackoff)
}

func channelActionOK() {
	channelActionMu.Lock()
	channelActionBackoff, channelActionHoldUntil = 0, time.Time{}
	channelActionMu.Unlock()
	channelActionWarned.Store(false)
}

func channelActionHeld(now time.Time) bool {
	channelActionMu.Lock()
	defer channelActionMu.Unlock()
	return now.Before(channelActionHoldUntil)
}

// logChannelActionFailure logs a transient failure: Warn once, then Info.
func logChannelActionFailure(err error) {
	if channelActionWarned.CompareAndSwap(false, true) {
		logging.L().Warnf("cloudsync: channel order actions not sent (kept, retried later): %v", err)
	} else {
		logging.L().Infof("cloudsync: channel order actions not sent (kept, retried later): %v", err)
	}
}

// uploadChannelOrderActions uploads the queued actions when the gate is open.
// It never returns an error: see the file comment.
// blockStrandedChannelOrderActions: a till that can no longer send (handed
// over to an additional till, or no longer enrolled) must not keep pending
// rows nobody can dismiss. They become blocked (main_till_required, the
// existing banner + "Dismiss unsent actions") and ONE Problem names the
// count; with nothing pending it is silent, so it never repeats per tick
// (ut-docs#4768, review finding 2).
func blockStrandedChannelOrderActions(ctx context.Context, db *sql.DB, why string) {
	n, err := data.NewChannelOrderActionsRepo(db).BlockPending(ctx, "main_till_required")
	if err != nil {
		logging.L().Warnf("cloudsync: channel order actions: %v", err)
		return
	}
	if n > 0 {
		logging.L().WarnProblemf(problemKeyChannelActionsStranded, "cloudsync: channel order actions: %s; %d unsent actions are kept on Online orders until staff dismiss them", why, n)
	}
}

const problemKeyChannelActionsStranded = "cloudsync.channel_actions_stranded"

func uploadChannelOrderActions(ctx context.Context, cfg *config.Config, db *sql.DB, hooks Hooks) {
	if hooks.ApplyChannelOrder == nil {
		return
	}
	m := enroll.Effective(cfg).Marketplace
	settings := data.NewSettingsRepo(db)
	if !enroll.CredentialsComplete(m) {
		blockStrandedChannelOrderActions(ctx, db, "this till is not enrolled with the cloud")
		return
	}
	if v, _, _ := settings.Get(ctx, "sync.primary_url"); strings.TrimSpace(v) != "" {
		blockStrandedChannelOrderActions(ctx, db, "this till is not the main till")
		return
	}
	now := time.Now()
	if channelActionHeld(now) {
		return
	}
	repo := data.NewChannelOrderActionsRepo(db)
	has, err := repo.HasSendable(ctx)
	if err != nil {
		logging.L().Warnf("cloudsync: channel order actions: %v", err)
		return
	}
	if !has {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, channelActionRunTimeout)
	defer cancel()

	// The existing apply path (pages.cloudApplyChannelOrder -> ChannelOrdersRepo.Apply):
	// a higher revision is applied, an equal one is a Duplicate, so the same
	// revision arriving later as a directive moves nothing twice.
	apply := func(ctx context.Context, actionID string, current map[string]any) error {
		dec, msg := decodeChannelOrder(directive{ID: "action:" + actionID, Type: "channel_order_update", Payload: current})
		if msg != "" {
			logging.L().Warnf("cloudsync: channel order action %s: the cloud's current order was refused (%s); the next directive brings it", actionID, msg)
			return fmt.Errorf("%w: %s", channelactions.ErrCurrentRefused, msg)
		}
		_, err := hooks.ApplyChannelOrder(ctx, dec)
		return err
	}
	sum, err := channelactions.Run(ctx, db, channelactions.NewHTTPPoster(httpClient, m.EndpointURL, m.StoreID, m.MerchantToken), apply, time.Now)
	var bad *channelactions.BadRequestError
	switch {
	case sum.Busy:
		return
	case errors.As(err, &bad):
		// A bad single action is a per-action refusal, never a 400: a 400 is a till bug.
		logging.L().Warnf("cloudsync: channel order actions: the cloud refused the request as malformed (400 %s) — a till bug; the actions are kept", bad.Code)
		channelActionFailed(now)
		return
	case err != nil:
		logChannelActionFailure(err)
		channelActionFailed(now)
		return
	case sum.Stopped != "":
		switch sum.Stopped {
		case "main_till_required":
			logging.L().Warnf("cloudsync: channel order actions: the cloud says this till is not the main till; %d unsent actions are kept on Online orders until staff dismiss them", sum.Blocked)
		case "device_credential_required":
			logging.L().Warnf("cloudsync: channel order actions: the cloud needs this till's own credential (%d %s); pair this till again; %d unsent actions are kept until staff dismiss them", sum.StopStatus, sum.Stopped, sum.Blocked)
		default:
			logging.L().Warnf("cloudsync: channel order actions: the cloud refused this till's actions (%d %s); %d unsent actions are kept until staff dismiss them", sum.StopStatus, sum.Stopped, sum.Blocked)
		}
		channelActionOK()
		return
	}
	channelActionOK()
	if sum.Sent > 0 {
		logging.L().Infof("cloudsync: channel order actions: %d sent (%d applied, %d refused, %d with a note)", sum.Sent, sum.Applied, sum.Refused, sum.Noted)
	}
}
