// Package channel is the Go guest SDK for sales-channel adapters: signed WASM
// plugins of canonical type `channel` that run in the ut-cloud channel host,
// never on a till (ADR-0169, ut-docs#4759; contract
// ut-docs reference/contracts/sales-channel-v1.md).
//
// A channel guest imports ONLY the "channel" host module below. It does not
// import the till's "ut" module, so a hostile or buggy adapter has no
// storage, no raw network, no settings and no secret to reach: its one
// network path is HTTPRequest, which the host proxies to the connection's
// verified account, adding the credential itself.
//
// Build with GOOS=wasip1 GOARCH=wasm. Under any other GOOS the host calls go
// to the FakeHost installed with UseFakeHost, so adapter logic is unit
// tested with plain `go test`.
package channel

// ContractVersion is the manifest's `channel_contract`.
const ContractVersion = 1

// HostModule is the wasm import module every function below lives in.
const HostModule = "channel"

// Host function names (contract section 3). The cloud channel host
// (ut-cloud internal/channelhost) exports exactly these;
// ut-cloud's channelhost TestHostFunctionsMirrorPOS reads raw_wasip1.go and
// fails when the two sides differ.
const (
	FnHTTP       = "channel_http"
	FnCursorGet  = "cursor_get"
	FnCursorSet  = "cursor_set"
	FnSubmit     = "channel_submit"
	FnOutboxNext = "outbox_next"
	FnOutboxAck  = "outbox_ack"
	FnLog        = "log"
)

// Asks (contract section 4): the event types the host sends the guest.
const (
	AskConnectTest   = "channel.connect.test.ask"
	AskOAuthCallback = "channel.oauth.callback.ask"
	AskWebhook       = "channel.webhook.ask"
	AskContact       = "channel.contact.ask"
	AskFetch         = "channel.fetch.ask"
	AskImport        = "channel.import.ask"
	AskOutbox        = "channel.outbox.ask"
	AskTick          = "channel.tick"
)

// Limits the host enforces (contract sections 3 and 4). The SDK refuses a
// call over a limit before it reaches the host, with ErrInvalid.
const (
	MaxBatchRecords = 500      // records per channel_submit
	MaxCursorBytes  = 64 << 10 // all cursor values of a connection
	MaxMemoryBytes  = 64 << 20 // guest linear memory
	DefaultCallSecs = 30       // deadline per call
	ImportCallSecs  = 300      // deadline per channel.import.ask page
	MaxOutboxBatch  = 100      // outbox_next max
)

// Kind of a channel (manifest channel.kind).
const (
	KindWebshop     = "webshop"
	KindMarketplace = "marketplace"
)

// Capability entities and directions (manifest channel.capabilities).
const (
	CapItems      = "items"
	CapVariants   = "variants"
	CapCategories = "categories"
	CapImages     = "images"
	CapStock      = "stock"
	CapOrders     = "orders"
	CapFulfilment = "fulfilment"
	CapRefunds    = "refunds"
	CapCancel     = "cancel"
	CapCustomers  = "customers"
	CapCompareAt  = "compare_at"
	CapItemCreate = "item_create" // only push is read; absent = cannot create products

	DirPush = "push" // UT to channel
	DirPull = "pull" // channel to UT
	DirBoth = "both"
)

// Record kinds accepted by Submit (contract section 2).
const (
	KindListing       = "listing"
	KindCategory      = "category"
	KindStockObserved = "stock_observed"
	KindOrder         = "order"
	KindOrderUpdate   = "order_update"
	KindRefund        = "refund"
	KindCustomer      = "customer"
)

// Outbox operation kinds (contract section 3). There is deliberately no
// delete operation: nothing is ever deleted at a channel.
const (
	OpListingUpsert    = "listing.upsert"
	OpListingUnpublish = "listing.unpublish"
	OpStockSet         = "stock.set"
	OpFulfilmentCreate = "fulfilment.create"
	OpOrderCancel      = "order.cancel"
	OpRefundCreate     = "refund.create"
	OpImageUpsert      = "image.upsert"
)

// Neutral webhook events a channel.webhook.ask answers with. The host never
// acts on a channel's own topic names.
const (
	EventOrderChanged   = "order_changed"
	EventUninstalled    = "uninstalled"
	EventCustomerRedact = "customer_redact"
	EventShopRedact     = "shop_redact"
	EventDataRequest    = "data_request"
	EventOther          = "other"
)

// Log levels.
const (
	LevelDebug = 0
	LevelInfo  = 1
	LevelWarn  = 2
	LevelError = 3
)

// Submit result statuses, one per record.
const (
	ResultAccepted  = "accepted"
	ResultUnchanged = "unchanged"
	ResultRejected  = "rejected"
)
