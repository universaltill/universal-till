package bluetooth

import "github.com/godbus/dbus/v5"

// agentCapability is what this process tells BlueZ it can do during a
// pairing: nothing. NoInputNoOutput selects "Just Works" pairing — no
// PIN, no passkey display, no yes/no prompt — which is what Bluetooth HID
// scanners and scales overwhelmingly support, and the only thing a
// background D-Bus call made from a web handler could honour anyway (there
// is no screen on the D-Bus side to show a passkey on). Documented in
// ADR-0078 as the deliberate scope of this feature.
const agentCapability = "NoInputNoOutput"

// agent is the org.bluez.Agent1 object BlueZ calls back into while a
// pairing is in flight. Exported per Pair() call and scoped to exactly the
// device the manager clicked on: with a NoInputNoOutput capability BlueZ
// rarely asks anything at all, but when it does (a device that insists on
// the legacy confirmation/authorization path), the answer is "yes" only
// for `expect` and "rejected" for any other device that happens to be
// trying to pair with this till at the same moment. Anything that needs
// a PIN or passkey typed in is refused outright — there is nowhere to type
// it, and silently answering "0000" would be a security hole, not a
// convenience.
//
// Method signatures follow godbus's export rules (last return value
// *dbus.Error) and the Agent1 interface from BlueZ's doc/agent-api.txt.
type agent struct {
	expect dbus.ObjectPath
}

func rejected() *dbus.Error {
	return dbus.NewError("org.bluez.Error.Rejected", nil)
}

func (a *agent) check(device dbus.ObjectPath) *dbus.Error {
	if device != a.expect {
		return rejected()
	}
	return nil
}

// Release is called when the agent is unregistered — nothing to clean up.
func (a *agent) Release() *dbus.Error { return nil }

// RequestPinCode: legacy PIN pairing. Refused — see the type comment.
func (a *agent) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	return "", rejected()
}

// DisplayPinCode: the device wants us to show a PIN for the user to type on
// IT (keyboard-style pairing). There is nowhere to show it — same
// NoInputNoOutput constraint as RequestPinCode — so this is refused
// outright, like RequestPinCode, rather than acknowledged. Acknowledging
// used to let the pairing continue as if informational, but nothing on
// this end can ever complete a code-entry pairing: BlueZ would then wait on
// a device that will never see its PIN typed, and the pairing dies on the
// 45s handler timeout instead of failing fast with an accurate "needs a
// PIN, can't pair here" message (ut-docs#1582 independent-review finding).
func (a *agent) DisplayPinCode(device dbus.ObjectPath, pincode string) *dbus.Error {
	return rejected()
}

// RequestPasskey: numeric passkey entry. Refused — see the type comment.
func (a *agent) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	return 0, rejected()
}

// DisplayPasskey: refused outright, same reasoning as DisplayPinCode —
// there is nowhere to show a passkey, and acknowledging only delays the
// same dead-end pairing to the 45s timeout instead of failing fast.
func (a *agent) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	return rejected()
}

// RequestConfirmation: "does this passkey match?" — accepted for the one
// device being paired (the manager already confirmed by clicking Pair on
// it; there is no second screen to compare a number on).
func (a *agent) RequestConfirmation(device dbus.ObjectPath, passkey uint32) *dbus.Error {
	return a.check(device)
}

// RequestAuthorization: incoming pairing with no passkey at all.
func (a *agent) RequestAuthorization(device dbus.ObjectPath) *dbus.Error {
	return a.check(device)
}

// AuthorizeService: the device wants to use a profile (for a scanner, the
// HID service UUID). Accepted for the device being paired only.
func (a *agent) AuthorizeService(device dbus.ObjectPath, uuid string) *dbus.Error {
	return a.check(device)
}

// Cancel: BlueZ withdrew a pending request — nothing is pending here.
func (a *agent) Cancel() *dbus.Error { return nil }
