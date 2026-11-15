package main

import (
	"html/template"
	"io/fs"
	"testing/fstest"
)

// fstestMap is a tiny in-memory locale dir: file name -> JSON text.
type fstestMap map[string]string

func (m fstestMap) Open(name string) (fs.File, error) {
	mf := fstest.MapFS{}
	for k, v := range m {
		mf[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return mf.Open(name)
}

// recordingFS records every name opened, to prove a bad lang code never
// reaches the file system.
type recordingFS struct {
	files  map[string]string
	opened []string
}

func (r *recordingFS) Open(name string) (fs.File, error) {
	r.opened = append(r.opened, name)
	return fstestMap(r.files).Open(name)
}

func htmlEscapeForTest(s string) string { return template.HTMLEscapeString(s) }
