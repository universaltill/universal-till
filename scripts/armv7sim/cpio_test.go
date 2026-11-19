package main

import (
	"bytes"
	"strconv"
	"testing"
)

func field(t *testing.T, b []byte, i int) int64 {
	t.Helper()
	v, err := strconv.ParseInt(string(b[6+8*i:6+8*(i+1)]), 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCpioNewc(t *testing.T) {
	var buf bytes.Buffer
	err := writeCpio(&buf, []cpioFile{
		{Name: "init", Mode: 0o755, Data: []byte("abcde")},
		{Name: "seed", Mode: 0o644, Data: []byte("xy")},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if len(b)%4 != 0 {
		t.Errorf("archive length %d is not 4-byte aligned", len(b))
	}
	if string(b[:6]) != "070701" {
		t.Fatalf("magic = %q", b[:6])
	}
	// header #0: ino=1 mode=0100755 filesize=5 namesize=5 ("init\0")
	if got := field(t, b, 0); got != 1 {
		t.Errorf("ino = %d", got)
	}
	if got := field(t, b, 1); got != 0o100755 {
		t.Errorf("mode = %o", got)
	}
	if got := field(t, b, 6); got != 5 {
		t.Errorf("filesize = %d", got)
	}
	if got := field(t, b, 11); got != 5 {
		t.Errorf("namesize = %d", got)
	}
	// name "init\0" ends at 110+5=115, padded to 116; data is 5 bytes then pad to 4.
	if string(b[110:114]) != "init" || b[114] != 0 {
		t.Errorf("name bytes = %q", b[110:115])
	}
	if string(b[116:121]) != "abcde" {
		t.Errorf("data = %q", b[116:121])
	}
	// next header starts at 124 (121 padded up)
	if string(b[124:130]) != "070701" {
		t.Errorf("second header at 124 = %q", b[124:130])
	}
	if !bytes.Contains(b, []byte("TRAILER!!!\x00")) {
		t.Error("no TRAILER!!! entry")
	}
	if !bytes.HasSuffix(bytes.TrimRight(b, "\x00"), []byte("TRAILER!!!")) {
		t.Error("trailer is not last")
	}
}
