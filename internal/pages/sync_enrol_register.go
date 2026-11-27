package pages

import (
	"context"
	"errors"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// enrolRegister is the register half of a LAN enrolment (ut-docs#7098, epic
// #7097): one cloud device = one register for life, so a register is created
// only for a device this till has never seen, and a device coming back keeps
// the one it had.
//
//   - replaceID != "": the enrolment replaces an existing till row; its register
//     stays (re-activated if it had been retired) and the link follows the new
//     device id.
//   - the device id is already linked to a register whose till row is gone AND a manager approved a request that named this
//     device (proof): the new enrolment takes that register over, and a changed
//     name renames it. A self-asserted device id alone never moves a register
//     (ut-docs#7097 review finding 1, AC13): without proof, or while the owning
//     till is live, the newcomer gets a register of its own, unlinked.
//   - otherwise a register is created and linked to the device ("" when the
//     till sent none).
//
// Fail open as the caller always did: the enrolment token is already burned,
// so any error degrades to the old behaviour (a register, possibly unlinked,
// or none) and is logged, never returned.
func enrolRegister(ctx context.Context, posRepo *data.POSRepo, replaceID, tillID, name, deviceID string, proof bool) (registerID, registerName string, takenOver bool) {
	if replaceID != "" {
		link, ok, err := posRepo.RegisterByTill(ctx, replaceID)
		if err != nil || !ok || link.RegisterID == "" {
			return "", "", false
		}
		registerID = link.RegisterID
		if link.RetiredReason != "" || registerInactive(ctx, posRepo, registerID) {
			if err := posRepo.ReactivateRegister(ctx, registerID, ""); err != nil {
				logging.L().Warnf("sync: re-activate register %s of re-enrolled till %s: %v", registerID, tillID, err)
			}
		}
		if deviceID != "" {
			if err := posRepo.LinkRegisterDevice(ctx, nil, registerID, deviceID, tillID, data.RegisterLinkEnrol); err != nil {
				logging.L().Warnf("sync: link register %s to device of till %s: %v", registerID, tillID, err)
			}
		}
		return registerID, "", false
	}

	if deviceID != "" {
		link, found, err := posRepo.RegisterByDevice(ctx, deviceID)
		if err != nil {
			logging.L().Warnf("sync: look up register of device for till %s: %v", tillID, err)
		} else if found && proof {
			live, lerr := posRepo.RegisterOwnerTillLive(ctx, link)
			if lerr == nil && !live {
				if err := posRepo.ReactivateRegister(ctx, link.RegisterID, ""); err == nil {
					if err := posRepo.LinkRegisterDevice(ctx, nil, link.RegisterID, deviceID, tillID, data.RegisterLinkEnrol); err == nil {
						registerName = takeOverName(ctx, posRepo, link.RegisterID, name)
						return link.RegisterID, registerName, true
					}
				}
			}
		}
	}

	id, regName, err := posRepo.CreateRegisterForEnrolment(ctx, name)
	if err != nil {
		logging.L().Errorf("auto-provision register for enrolling till %s: %v", tillID, err)
		return "", "", false
	}
	if err := posRepo.LinkRegisterDevice(ctx, nil, id, deviceID, tillID, data.RegisterLinkEnrol); err != nil {
		if errors.Is(err, data.ErrDeviceHasRegister) {
			logging.L().Warnf("sync: device of till %s already has a register; the new register %s stays unlinked", tillID, id)
		} else {
			logging.L().Warnf("sync: link new register %s of till %s: %v", id, tillID, err)
		}
	}
	return id, regName, false
}

// takeOverName renames a taken-over register to the name the manager approved
// when that differs, keeping the old name if the new one is taken. Returns the
// register's name afterwards ("" when it could not be read).
func takeOverName(ctx context.Context, posRepo *data.POSRepo, registerID, name string) string {
	if strings.TrimSpace(name) != "" {
		if err := posRepo.RenameRegister(ctx, registerID, name); err != nil {
			logging.L().Warnf("sync: rename taken-over register %s: %v", registerID, err)
		}
	}
	regs, err := posRepo.ListRegistersForAdmin(ctx)
	if err != nil {
		return ""
	}
	for _, r := range regs {
		if r.ID == registerID {
			return r.Name
		}
	}
	return ""
}

func registerInactive(ctx context.Context, posRepo *data.POSRepo, registerID string) bool {
	regs, err := posRepo.ListRegistersForAdmin(ctx)
	if err != nil {
		return false
	}
	for _, r := range regs {
		if r.ID == registerID {
			return !r.IsActive
		}
	}
	return false
}
