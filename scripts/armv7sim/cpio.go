package main

import (
	"fmt"
	"io"
)

type cpioFile struct {
	Name string
	Mode uint32 // permission bits; the regular-file type is added
	Data []byte
}

// writeCpio writes an uncompressed "newc" (070701) archive the kernel unpacks
// as its initramfs. Uncompressed on purpose: no gunzip under TCG.
func writeCpio(w io.Writer, files []cpioFile) error {
	n := 0
	put := func(name string, mode uint32, data []byte, ino int) error {
		// magic + 13 fields of 8 hex digits: ino mode uid gid nlink mtime
		// filesize devmajor devminor rdevmajor rdevminor namesize check
		if _, err := fmt.Fprintf(w, "070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
			ino, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(name)+1, 0); err != nil {
			return err
		}
		if _, err := io.WriteString(w, name+"\x00"); err != nil {
			return err
		}
		if err := pad(w, 110+len(name)+1); err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		return pad(w, len(data))
	}
	for _, f := range files {
		n++
		if err := put(f.Name, 0o100000|f.Mode, f.Data, n); err != nil {
			return err
		}
	}
	return put("TRAILER!!!", 0, nil, 0)
}

func pad(w io.Writer, n int) error {
	if r := n % 4; r != 0 {
		_, err := w.Write(make([]byte, 4-r))
		return err
	}
	return nil
}
