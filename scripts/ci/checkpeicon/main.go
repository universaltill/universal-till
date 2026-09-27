// Command checkpeicon fails unless every given Windows exe carries the
// resources ut-docs#2786 embeds (packaging/windows/winres/):
//
//   - an RT_GROUP_ICON with integer ID 32512 (IDI_APPLICATION) — the
//     vendored webview loads its window icon by exactly that ID, and
//     Explorer, the shortcuts and Apps & features use it as icon index 0;
//   - at least one RT_ICON (the images the group points at);
//   - an RT_VERSION (VERSIONINFO: product, company, version).
//
// Without them Windows shows its generic icon everywhere, which is the bug
// the card reported. release.yml runs this on the exes in the portable zip;
// ci.yml runs it on a cross-compiled unitill-pos.exe.
//
//	go run ./scripts/ci/checkpeicon <exe>...
package main

import (
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16

	// appIconID is IDI_APPLICATION, MAKEINTRESOURCE(32512).
	appIconID = 32512

	imageDirectoryEntryResource = 2
)

// resourceDir is the parsed shape of one IMAGE_RESOURCE_DIRECTORY level:
// integer IDs only (named entries are skipped; nothing here uses names).
type resourceDir struct {
	ids map[uint32]uint32 // ID -> OffsetToData (high bit set = subdirectory)
}

// readDir parses the IMAGE_RESOURCE_DIRECTORY at off within rsrc.
func readDir(rsrc []byte, off uint32) (resourceDir, error) {
	const hdr = 16
	if uint64(off)+hdr > uint64(len(rsrc)) {
		return resourceDir{}, fmt.Errorf("resource directory at %#x is out of bounds", off)
	}
	named := uint32(binary.LittleEndian.Uint16(rsrc[off+12:]))
	ids := uint32(binary.LittleEndian.Uint16(rsrc[off+14:]))
	end := uint64(off) + hdr + uint64(named+ids)*8
	if end > uint64(len(rsrc)) {
		return resourceDir{}, fmt.Errorf("resource directory at %#x has entries out of bounds", off)
	}
	d := resourceDir{ids: map[uint32]uint32{}}
	for i := named; i < named+ids; i++ {
		e := off + hdr + i*8
		d.ids[binary.LittleEndian.Uint32(rsrc[e:])] = binary.LittleEndian.Uint32(rsrc[e+4:])
	}
	return d, nil
}

// resourceSection returns the bytes of the resource directory tree; its
// internal offsets are relative to the returned slice's start.
func resourceSection(f *pe.File) ([]byte, error) {
	var va, size uint32
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		if oh.NumberOfRvaAndSizes > imageDirectoryEntryResource {
			va, size = oh.DataDirectory[imageDirectoryEntryResource].VirtualAddress, oh.DataDirectory[imageDirectoryEntryResource].Size
		}
	case *pe.OptionalHeader32:
		if oh.NumberOfRvaAndSizes > imageDirectoryEntryResource {
			va, size = oh.DataDirectory[imageDirectoryEntryResource].VirtualAddress, oh.DataDirectory[imageDirectoryEntryResource].Size
		}
	default:
		return nil, errors.New("no PE optional header")
	}
	if va == 0 || size == 0 {
		return nil, errors.New("no resource directory (RT_GROUP_ICON missing): the .syso from packaging/windows/winres.sh was not linked")
	}
	for _, s := range f.Sections {
		if va >= s.VirtualAddress && va < s.VirtualAddress+s.VirtualSize {
			data, err := s.Data()
			if err != nil {
				return nil, fmt.Errorf("read section %s: %w", s.Name, err)
			}
			start := va - s.VirtualAddress
			if uint64(start) >= uint64(len(data)) {
				return nil, fmt.Errorf("resource directory lies past section %s's raw data", s.Name)
			}
			return data[start:], nil
		}
	}
	return nil, fmt.Errorf("resource directory RVA %#x is in no section", va)
}

func check(path string) error {
	f, err := pe.Open(path)
	if err != nil {
		return fmt.Errorf("%s: not a PE file: %w", path, err)
	}
	defer f.Close()

	rsrc, err := resourceSection(f)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	root, err := readDir(rsrc, 0)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	group, ok := root.ids[rtGroupIcon]
	if !ok || group&0x80000000 == 0 {
		return fmt.Errorf("%s: no RT_GROUP_ICON resource — Windows would show the generic icon", path)
	}
	groups, err := readDir(rsrc, group&0x7fffffff)
	if err != nil {
		return fmt.Errorf("%s: RT_GROUP_ICON: %w", path, err)
	}
	if _, ok := groups.ids[appIconID]; !ok {
		return fmt.Errorf("%s: RT_GROUP_ICON has no ID %d (IDI_APPLICATION) — the webview window would have no icon", path, appIconID)
	}
	if _, ok := root.ids[rtIcon]; !ok {
		return fmt.Errorf("%s: RT_GROUP_ICON present but no RT_ICON images", path)
	}
	if _, ok := root.ids[rtVersion]; !ok {
		return fmt.Errorf("%s: no RT_VERSION (VERSIONINFO) resource", path)
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: checkpeicon <exe>...")
		os.Exit(2)
	}
	failed := false
	for _, p := range os.Args[1:] {
		if err := check(p); err != nil {
			fmt.Fprintln(os.Stderr, "::error::"+err.Error())
			failed = true
			continue
		}
		fmt.Printf("checkpeicon: %s has RT_GROUP_ICON %d, RT_ICON and RT_VERSION\n", p, appIconID)
	}
	if failed {
		os.Exit(1)
	}
}
