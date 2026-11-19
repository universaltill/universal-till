package offsitebackup

import (
	"context"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// Scheduler runs the uploader on its own goroutine: five minutes after boot,
// then hourly (the round itself honours the backoff and the 20 h rule), plus
// whenever Trigger is called. It shares nothing with the sale path.
type Scheduler struct {
	U        *Uploader
	First    time.Duration // default 5 min
	Interval time.Duration // default 1 h

	trigger chan struct{}
	once    sync.Once
}

func (s *Scheduler) init() { s.once.Do(func() { s.trigger = make(chan struct{}, 1) }) }

// Trigger asks for a round now ("Upload now"); it never blocks.
func (s *Scheduler) Trigger() {
	s.init()
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	s.init()
	first, every := s.First, s.Interval
	if first <= 0 {
		first = 5 * time.Minute
	}
	if every <= 0 {
		every = time.Hour
	}
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		force := false
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-s.trigger:
			force = true
		}
		func() {
			defer logging.RecoverAndLog("offsitebackup.round")
			if err := s.U.Round(ctx, force); err != nil && ctx.Err() == nil {
				logging.L().Warnf("offsitebackup: round: %v", err)
			}
		}()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(every)
	}
}
