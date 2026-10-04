package cloudsync

import (
	"strings"

	"github.com/universaltill/universal-till/internal/enroll"
)

// Device-targeted directives (ut-docs#3272 rename_till, ut-docs#2537
// print_report, ut-docs#3466 kiosk_unlock — ADR-0142 D3): each names one
// till in its payload's device_id. The cloud serves one only to that
// device's own sync, and the till checks the target itself too (defence in
// depth): one whose device_id is blank or another till's is skipped
// outright — no apply, no result post — so it stays pending for its real
// target. All three are any-till (none is in mainTillOnlyTypes).
//
// ADR-0142 D3 asked for this check to be generalised rather than copied
// once more per type: a new targeted type joins deviceTargetedTypes, and
// Tick needs no new branch.
var deviceTargetedTypes = map[string]bool{
	"rename_till":  true,
	"print_report": true,
	"kiosk_unlock": true,
}

// ownDeviceID is the device id this till reports in its heartbeat (the id
// the cloud addresses targeted directives to). A var only so a test can
// swap it.
var ownDeviceID = func() string { return enroll.CurrentStatus().DeviceID }

// targetSkipLog: a targeted directive addressed elsewhere logs once per id.
var targetSkipLog skipOnceLog

func firstTargetSkip(id string) bool { return targetSkipLog.first(id) }

// deviceTargetSkipReason reports why this till must skip d, or "" to apply
// it: a device-targeted directive whose device_id is blank or another
// till's, or one that arrives before this till knows its own id. A type
// that names no device is not this check's business ("").
func deviceTargetSkipReason(d directive) string {
	if !deviceTargetedTypes[d.Type] {
		return ""
	}
	target, _ := d.Payload["device_id"].(string)
	target = strings.TrimSpace(target)
	self := strings.TrimSpace(ownDeviceID())
	switch {
	case self == "":
		return "this till's own device id is not known yet"
	case target == "" || target != self:
		return "addressed to another till"
	}
	return ""
}
