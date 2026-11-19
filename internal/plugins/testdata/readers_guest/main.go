//go:build wasip1

// Test guest for ut-docs#6566 (multi-reader card payments): a payment plugin
// that declares payment.<key>.readers. It answers the reader state read-only
// op from the reader id's own text, so an e2e/docs run can drive every state
// without a provider: an id containing "offline" is offline, "busy" busy,
// "setup" needs_setup, "slow" never answers in time (the till shows unknown),
// anything else is ready. authorize approves with a fake transaction_ref and
// echoes nothing sensitive; refund approves. Generic names only.
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/universaltill/universal-till/sdk/plugin"
)

func main() {
	plugin.Run(plugin.Handlers{
		"*": func(e plugin.Event) (any, error) {
			switch {
			case strings.HasSuffix(e.Type, ".readers"):
				var p struct {
					Readers []string `json:"readers"`
				}
				_ = e.Decode(&p)
				var items []string
				for _, id := range p.Readers {
					state, reason := "ready", ""
					switch {
					case strings.Contains(id, "slow"):
						time.Sleep(5 * time.Second)
					case strings.Contains(id, "offline"):
						state, reason = "offline", "reader_unreachable"
					case strings.Contains(id, "busy"):
						state = "busy"
					case strings.Contains(id, "setup"):
						state, reason = "needs_setup", "reader_needs_setup"
					}
					item := fmt.Sprintf(`{"reader_id":%q,"state":%q`, id, state)
					if reason != "" {
						item += fmt.Sprintf(`,"reason":%q`, reason)
					}
					items = append(items, item+"}")
				}
				return []byte(`{"readers":[` + strings.Join(items, ",") + "]}\n"), nil
			case strings.HasSuffix(e.Type, ".authorize"):
				return []byte(`{"outcome":"approved","transaction_ref":"txn_e2e_` + e.ID[:8] + `"}` + "\n"), nil
			case strings.HasSuffix(e.Type, ".refund"):
				return []byte(`{"outcome":"refunded"}` + "\n"), nil
			}
			return nil, nil
		},
	})
}
