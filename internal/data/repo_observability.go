package data

import (
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// repoObservability centralizes logging/error wrapping for repositories.
type repoObservability struct {
	name string
	log  *logging.Logger
}

func newRepoObservability(name string) repoObservability {
	return repoObservability{name: name, log: logging.L()}
}

func (o repoObservability) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	if o.log != nil {
		o.log.Errorf("data.%s.%s: %v", o.name, op, err)
	}
	return fmt.Errorf("%s.%s: %w", o.name, op, err)
}

func (o repoObservability) wrapf(op, format string, err error, args ...any) error {
	if err == nil {
		return nil
	}
	if o.log != nil {
		o.log.Errorf("data.%s.%s: %v", o.name, op, err)
	}
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
}

func (o repoObservability) trace(op string) func(error) {
	started := time.Now()
	return func(err error) {
		if o.log == nil {
			return
		}
		dur := time.Since(started)
		if err != nil {
			o.log.Errorf("data.%s.%s failed in %s: %v", o.name, op, dur, err)
			return
		}
		o.log.Debugf("data.%s.%s ok in %s", o.name, op, dur)
	}
}
