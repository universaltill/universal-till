package bluetooth

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
)

// NewDBusClient connects to the D-Bus system bus and returns a Client
// driving org.bluez over it. A box with no system bus at all (a developer
// laptop that isn't Linux, a container) fails here with ErrUnavailable; a
// box with a bus but no bluetoothd fails on the first call instead — both
// degrade to the same status notice in the panel, never a startup failure
// for the till (ADR-0078 consequences).
//
// One connection per call site, not a process-wide singleton: BlueZ ties
// both discovery sessions and agent registrations to the D-Bus connection
// that made them, so a handler that opens, scans and closes can never leave
// the adapter stuck in discovery mode or a stale agent registered if the
// request dies half way. The system bus is a local unix socket — the
// connect is microseconds, not a cost worth pooling.
func NewDBusClient() (Client, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return newClient(&dbusBus{conn: conn}), nil
}

// dbusBus is the real transport behind the bus seam.
type dbusBus struct {
	conn *dbus.Conn
}

func (b *dbusBus) ManagedObjects(ctx context.Context) (managedObjects, error) {
	var out managedObjects
	err := b.conn.Object(bluezService, "/").
		CallWithContext(ctx, "org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).
		Store(&out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (b *dbusBus) Call(ctx context.Context, path dbus.ObjectPath, method string, args ...any) error {
	return b.conn.Object(bluezService, path).CallWithContext(ctx, method, 0, args...).Err
}

func (b *dbusBus) SetProperty(ctx context.Context, path dbus.ObjectPath, iface, prop string, value any) error {
	return b.conn.Object(bluezService, path).
		CallWithContext(ctx, "org.freedesktop.DBus.Properties.Set", 0, iface, prop, dbus.MakeVariant(value)).
		Err
}

func (b *dbusBus) ExportAgent(a *agent, path dbus.ObjectPath) error {
	// Export replaces any object previously exported at this path on this
	// connection, so a second Pair on the same client is fine.
	return b.conn.Export(a, path, "org.bluez.Agent1")
}

func (b *dbusBus) Close() error { return b.conn.Close() }
