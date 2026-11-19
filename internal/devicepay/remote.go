package devicepay

import "context"

// ut-docs#6538 R1b: a tender paid on a paired phone reader is collected by
// core's remote collector (internal/readerlink) instead of this device's
// native bridge. The choice is made by core's payment gate (a Binding naming
// the registry reader), never by the plugin or the request.

// RemoteCollector takes one payment on the card reader ref (its registry
// reader ref) and answers the same advisory Result a native bridge does.
type RemoteCollector interface {
	CollectRemote(ctx context.Context, ref string, req Request) Result
}

// Reasons the remote path itself produces.
const (
	ReasonReaderUnreachable = "reader_unreachable" // no live, ready phone link: nothing was started
	ReasonReaderBusy        = "reader_busy"        // the phone is collecting another payment: nothing was started
	ReasonLinkLost          = "link_lost"          // the link dropped mid-collect: the customer may have been charged
)

var remote RemoteCollector // guarded by mu

// SetRemote registers the remote collector; nil un-registers it.
func SetRemote(c RemoteCollector) {
	mu.Lock()
	defer mu.Unlock()
	remote = c
}

type readerKey struct{}

// WithReader marks ctx so Collect goes to the remote collector for ref. Only
// core's payment path sets it, from the gate's binding; a plugin or a request
// cannot.
func WithReader(ctx context.Context, ref string) context.Context {
	return context.WithValue(ctx, readerKey{}, ref)
}

func readerFrom(ctx context.Context) string {
	ref, _ := ctx.Value(readerKey{}).(string)
	return ref
}

// collectRemote is Collect for a tender paid on a phone reader: never the
// native bridge, whatever is registered. No remote collector: nothing was
// started.
func collectRemote(ctx context.Context, ref string, req Request) Result {
	mu.RLock()
	r := remote
	mu.RUnlock()
	if r == nil {
		return Result{Status: StatusDeclined, Reason: ReasonReaderUnreachable}
	}
	return r.CollectRemote(ctx, ref, req)
}
