package tills

// MarkSameDevice sets Row.SameDeviceAs on an old entry that is provably the
// same physical device as a live one (ut-docs#7098): a device that joined again
// under a new name and left the first entry behind. Pure; no names are
// compared, because two shops can have two phones called "phone".
//
// An old entry (old_entry, local_only unseen for a while, or cloud_only) is
// marked when
//   - a live entry (ok, or this till) holds the same non-empty device id, or
//   - supersedes[liveDeviceID] == the old entry's device id: the register link
//     of the live device says it took over from exactly that device.
//
// Only the first matching live entry (in list order) is named.
func MarkSameDevice(rows []Row, supersedes map[string]string) {
	live := func(r Row) bool { return r.IsSelf || r.State == StateOK || r.State == StateRoleMismatch }
	devs := func(r Row) []string {
		var out []string
		for _, d := range []string{r.DeviceID, r.CloudDeviceID} {
			if d != "" {
				out = append(out, d)
			}
		}
		return out
	}
	for i := range rows {
		old := &rows[i]
		if old.IsSelf || live(*old) || old.State == StateReplaced {
			continue
		}
		for _, od := range devs(*old) {
			for _, lv := range rows {
				if lv.ID == old.ID || !live(lv) {
					continue
				}
				for _, ld := range devs(lv) {
					if ld == od || supersedes[ld] == od {
						old.SameDeviceAs = lv.Name
						break
					}
				}
				if old.SameDeviceAs != "" {
					break
				}
			}
			if old.SameDeviceAs != "" {
				break
			}
		}
	}
}
