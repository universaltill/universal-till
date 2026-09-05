package pages

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Order-status stream bridge, replica side (ADR-0079, ut-docs#1571).
//
// common.Deps.OrderStatus is per-process, and on a replica /ui/orders doesn't
// even read local state while the primary is reachable — it proxies to the
// primary's board (ut-docs#1350). So a push has to cross that same
// primary↔replica boundary: this goroutine holds
// GET {primary}/api/sync/orders/stream open with the sync bearer (the exact
// trust boundary fetchOrdersFromPrimary already uses) and republishes every
// event it receives onto THIS till's own broadcaster, so the replica's
// browser-facing /api/orders/stream fans it out exactly as if the tap had
// happened here. A page never needs to know which till it's running on.
//
// Shape mirrors StartTSEProvisionRetry exactly — wg.Add(1)/defer wg.Done(),
// ctx.Done() for shutdown, silent-and-retry — and the failure stance is
// fetchOrdersFromPrimary's: Debugf only (a 15s-poll-cadence Info per miss
// would flood the Problems ring), bounded exponential backoff, never a busy
// loop, and never anything a sale or a status tap depends on to complete
// (offline-first, ADR-0003: the 15s poll is untouched and still the floor).

// Bridge cadence. Vars, not consts, only so the bridge tests can run at
// millisecond scale; production values are what's written here.
var (
	// orderStreamBridgeRecheck: how often a till that is NOT (yet) a replica
	// re-reads its sync settings. Enrolment happens after boot, so a till
	// that becomes a replica later must pick this up without a restart.
	orderStreamBridgeRecheck = 30 * time.Second
	// orderStreamBridgeBackoffMin/Max bound the retry delay after any
	// failure (dial error, non-200, stream dropped): start at Min, double,
	// cap at Max, reset to Min on any successful connect.
	orderStreamBridgeBackoffMin = 2 * time.Second
	orderStreamBridgeBackoffMax = 30 * time.Second
	// orderStreamBridgeIdleCutoff: a connected stream that goes completely
	// silent for this long is treated as dead and re-dialled. MUST exceed
	// orderStreamHeartbeat (20s) by a comfortable margin — the heartbeat is
	// exactly what keeps a healthy-but-quiet stream from tripping this.
	orderStreamBridgeIdleCutoff = 60 * time.Second
)

// orderStreamClient is the bridge's client. Timeout MUST stay 0: a long-lived
// stream has no overall deadline — liveness is the idle cutoff above (reset
// on every byte, so a heartbeat is enough), not a whole-request timeout.
var orderStreamClient = &http.Client{Timeout: 0}

// StartOrderStatusStreamBridge launches the replica-side bridge. Wired in
// internal/pages/init.go alongside StartTSEProvisionRetry/StartCloudSync;
// app.Run's drain joins it via wg.
func StartOrderStatusStreamBridge(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	if d.OrderStatus == nil {
		return // bare Deps (tests): nothing to republish onto
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		backoff := orderStreamBridgeBackoffMin
		for {
			base, bearer, isReplica := replicaSyncTarget(ctx, d)
			if !isReplica {
				// Primary/standalone (or half-enrolled) till: nothing to
				// bridge. Re-check later — enrolment may happen at any time.
				if !sleepCtx(ctx, orderStreamBridgeRecheck) {
					return
				}
				continue
			}
			connected, err := runOrderStatusStreamOnce(ctx, d, base, bearer)
			if ctx.Err() != nil {
				return // shutdown, not a failure
			}
			if err != nil {
				logging.L().Debugf("orders stream bridge: %v — retrying in %s", err, backoff)
			}
			if connected {
				backoff = orderStreamBridgeBackoffMin
			}
			if !sleepCtx(ctx, backoff) {
				return
			}
			if !connected {
				backoff *= 2
				if backoff > orderStreamBridgeBackoffMax {
					backoff = orderStreamBridgeBackoffMax
				}
			}
		}
	}()
}

// sleepCtx waits d or until ctx is done; false means ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// runOrderStatusStreamOnce opens one stream on the primary and republishes
// every decoded event until the stream ends. connected=true means the
// primary answered 200 (so the caller resets its backoff even if the stream
// later dropped); err describes why the connection ended.
func runOrderStatusStreamOnce(ctx context.Context, d *common.Deps, base, bearer string) (connected bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/sync/orders/stream", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := orderStreamClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("primary unreachable (%w)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("primary answered %s", resp.Status)
	}

	// Idle cutoff: closing the body from the timer unblocks the reader with
	// an error, which the retry loop treats like any other dropped stream.
	// Every byte read (heartbeats included) pushes the deadline out.
	idle := time.AfterFunc(orderStreamBridgeIdleCutoff, func() { resp.Body.Close() })
	defer idle.Stop()
	body := &activityReader{r: resp.Body, touch: func() { idle.Reset(orderStreamBridgeIdleCutoff) }}

	err = readOrderStatusSSE(body, func(ev pos.OrderStatusChanged) {
		d.OrderStatus.Publish(ev)
	})
	if err == nil {
		err = fmt.Errorf("primary closed the stream")
	}
	return true, err
}

// activityReader calls touch on every successful Read — the idle-cutoff
// reset hook.
type activityReader struct {
	r     io.Reader
	touch func()
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.touch()
	}
	return n, err
}

// readOrderStatusSSE is a deliberately small line-based SSE reader: frames
// are separated by a blank line; `event:` names the frame, `data:` carries
// the payload; `: comment` lines (the heartbeat), blank lines and any other
// field (id, retry, …) are ignored. onEvent fires once per frame whose data
// decodes as a pos.OrderStatusChanged and whose event name is empty or
// "order-status" — a foreign event name (a future KDS frame on the same
// stream) or malformed data skips that frame and keeps reading; it never
// aborts the stream. Returns nil on clean EOF (a frame still pending at EOF
// is delivered), else the read error.
func readOrderStatusSSE(r io.Reader, onEvent func(pos.OrderStatusChanged)) error {
	sc := bufio.NewScanner(r)
	var event, data string
	flush := func() {
		defer func() { event, data = "", "" }()
		if data == "" || (event != "" && event != "order-status") {
			return
		}
		var ev pos.OrderStatusChanged
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return
		}
		onEvent(ev)
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, ":"):
			// comment / heartbeat
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				event = value
			case "data":
				// Multi-line data joins with \n per the SSE spec; the primary
				// only ever sends one line, but be spec-shaped anyway.
				if data != "" {
					data += "\n"
				}
				data += value
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	flush() // a final frame the primary didn't get to terminate before EOF
	return nil
}
