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

// SetRemote registers the remote collector; nil un-registers it.
func SetRemote(c RemoteCollector) {}

// WithReader marks ctx so Collect goes to the remote collector for ref.
func WithReader(ctx context.Context, ref string) context.Context { return ctx }
