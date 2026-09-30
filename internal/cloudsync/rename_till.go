package cloudsync

import (
	"strings"

	"github.com/universaltill/universal-till/internal/enroll"
)

// rename_till (ut-docs#3272): the cloud renames one till — main or
// additional — and serves the directive only to that device's own sync. The
// till checks the target itself too (defence in depth): a rename_till whose
// payload device_id is blank or another till's is skipped outright, with no
// apply and no result post, so it stays pending for its real target.

// ownDeviceID is the device id this till reports in its heartbeat (the id
// the cloud addresses rename_till to). A var only so a test can swap it.
var ownDeviceID = func() string { return enroll.CurrentStatus().DeviceID }

// renameSkipLog: a rename_till addressed elsewhere logs once per id.
var renameSkipLog skipOnceLog

func firstRenameSkip(id string) bool { return renameSkipLog.first(id) }

// renameTillSkipReason reports why this till must skip d, or "" to apply
// it: a rename_till whose device_id is blank or another till's, or one that
// arrives before this till knows its own id.
func renameTillSkipReason(d directive) string {
	if d.Type != "rename_till" {
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
