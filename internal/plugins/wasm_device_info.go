package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Device-info host functions (ADR-0140, ut-docs#3862): device_id_get,
// device_local_ips_get and device_timezone_get hand a plugin this till's
// identity — e.g. for a tax authority's fraud-prevention headers. Each
// needs the ★ review-gated, read-only, bare permission device-info
// (no wildcard or parameter form), checked on EVERY call so a revoke takes
// effect live.
//
// Grants are audited, not just denials (ADR-0140 D1): the values identify
// the device and its network, so every hand-out leaves a device_info_read
// row naming the function. Denials are audited by CheckPermission
// (permission_denied). If the grant's audit row can't be written the call
// fails closed — no identity leaves without an audit trail.
//
// All three use the buffer ABI (wasm_hostfns.go): min(len, dstCap) bytes
// are written and the FULL length returned. Returns -2 (device-info not
// declared or not granted), -3 (permission lookup, audit, settings or OS
// failure; a blank stored device id), -4 (unwritable guest memory).
//
//   - device_id_get: a v4 UUID minted on first use and kept under
//     data.DeviceInfoIDSettingsKey — never reset on re-pair, never derived
//     from or linked to marketplace.device_id, per till (never synced).
//   - device_local_ips_get: a JSON array of this host's interface
//     addresses, IPv4 and IPv6, loopback/unspecified excluded, zone
//     stripped, deduplicated, sorted. No address is `[]`, not an error.
//   - device_timezone_get: the local zone's offset in force at call time,
//     as UTC±hh:mm (e.g. UTC+01:00, UTC-05:30).

const deviceInfoPermission = "device-info"

// Seams for tests.
var (
	deviceInterfaceAddrs = net.InterfaceAddrs
	deviceNow            = time.Now
)

// deviceInfoAllowed checks device-info for this call. 0 means go ahead;
// otherwise the code to return: -2 for a denial (audited by
// CheckPermissionGranted), -3 when the lookup itself failed — a granted
// plugin must not read a DB error as a revoke.
func deviceInfoAllowed(ctx context.Context, s *hostState, function string) int32 {
	granted, err := CheckPermissionGranted(ctx, s.db, s.pluginID, deviceInfoPermission)
	if err != nil {
		logging.L().Errorf("[wasm:%s] %s: permission lookup failed (%v)", s.pluginID, function, err)
		return hostErrInternal
	}
	if !granted {
		return hostErrDenied
	}
	return 0
}

// deviceInfoHandOut audits a value about to be handed out (written only
// once the value exists, so a failed read is never recorded as a read) and
// writes it to the guest. If the audit row can't be written the call fails
// closed: no identity leaves without an audit trail.
func deviceInfoHandOut(ctx context.Context, m api.Module, s *hostState, function string, dstPtr, dstCap uint32, value []byte) int32 {
	if err := data.NewPluginRepo(s.db).InsertAudit(ctx, nil, "device_info_read", s.pluginID,
		map[string]any{"function": function}, time.Now()); err != nil {
		logging.L().Errorf("[wasm:%s] %s: audit write failed, refusing (%v)", s.pluginID, function, err)
		return hostErrInternal
	}
	return writeGuest(m, dstPtr, dstCap, value)
}

func hostDeviceIDGet(ctx context.Context, m api.Module, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	if code := deviceInfoAllowed(ctx, s, "device_id_get"); code != 0 {
		return code
	}
	id, err := data.NewSettingsRepo(s.db).GetOrCreate(ctx, data.DeviceInfoIDSettingsKey, uuid.NewString())
	if err != nil {
		logging.L().Errorf("[wasm:%s] device_id_get: %v", s.pluginID, err)
		return hostErrInternal
	}
	id = strings.TrimSpace(id)
	if id == "" {
		logging.L().Errorf("[wasm:%s] device_id_get: stored device id is blank, refusing", s.pluginID)
		return hostErrInternal
	}
	return deviceInfoHandOut(ctx, m, s, "device_id_get", dstPtr, dstCap, []byte(id))
}

func hostDeviceLocalIPsGet(ctx context.Context, m api.Module, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	if code := deviceInfoAllowed(ctx, s, "device_local_ips_get"); code != 0 {
		return code
	}
	out, err := deviceLocalIPsJSON()
	if err != nil {
		logging.L().Errorf("[wasm:%s] device_local_ips_get: %v", s.pluginID, err)
		return hostErrInternal
	}
	return deviceInfoHandOut(ctx, m, s, "device_local_ips_get", dstPtr, dstCap, out)
}

func hostDeviceTimezoneGet(ctx context.Context, m api.Module, dstPtr, dstCap uint32) int32 {
	s, ok := stateFrom(ctx)
	if !ok {
		return hostErrInternal
	}
	if code := deviceInfoAllowed(ctx, s, "device_timezone_get"); code != 0 {
		return code
	}
	return deviceInfoHandOut(ctx, m, s, "device_timezone_get", dstPtr, dstCap, []byte(formatUTCOffset(deviceNow())))
}

// deviceLocalIPsJSON lists this host's interface addresses as a JSON array
// (see device_local_ips_get above).
func deviceLocalIPsJSON() ([]byte, error) {
	addrs, err := deviceInterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("interface addresses: %w", err)
	}
	seen := map[netip.Addr]bool{}
	ips := []netip.Addr{}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP // its Zone is dropped here
		default:
			continue
		}
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		addr = addr.Unmap().WithZone("")
		if addr.IsLoopback() || addr.IsUnspecified() || seen[addr] {
			continue
		}
		seen[addr] = true
		ips = append(ips, addr)
	}
	slices.SortFunc(ips, func(a, b netip.Addr) int { return a.Compare(b) })
	strs := make([]string, len(ips))
	for i, ip := range ips {
		strs[i] = ip.String()
	}
	return json.Marshal(strs)
}

// formatUTCOffset renders t's zone offset as UTC±hh:mm.
func formatUTCOffset(t time.Time) string {
	_, off := t.Zone()
	sign := '+'
	if off < 0 {
		sign = '-'
		off = -off
	}
	return fmt.Sprintf("UTC%c%02d:%02d", sign, off/3600, (off%3600)/60)
}
