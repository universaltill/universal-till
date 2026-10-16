//go:build windows

package lantls

import "golang.org/x/sys/windows/registry"

// readHostID reads Windows' MachineGuid, set at install (a sysprep
// regenerates it; a plain disk clone keeps it).
func readHostID() (source, raw string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", ""
	}
	defer func() { _ = k.Close() }()
	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return "", ""
	}
	if id := cleanHostID(v); id != "" {
		return "machine-guid", id
	}
	return "", ""
}
