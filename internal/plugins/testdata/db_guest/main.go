//go:build wasip1

// Test guest for the plugin-owned SQLite host functions (ut-docs#3157). It is
// a real compiled wasip1 module: every db call crosses the wasm boundary and
// the host's linear-memory ABI. Raw imports because the guest SDK has no db
// binding. payload.mode picks the scenario; results land in plugin storage.
// The data statements come in payload.sql (name -> [statement, args JSON])
// from the test, so this file holds no query text (guard-data-access.sh).
package main

import (
	"encoding/json"
	"unsafe"

	"github.com/universaltill/universal-till/sdk/plugin"
)

//go:wasmimport ut db_exec
func rawDBExec(sPtr, sLen, aPtr, aLen uint32) int64

//go:wasmimport ut db_query
func rawDBQuery(sPtr, sLen, aPtr, aLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut db_error
func rawDBError(dstPtr, dstCap uint32) int32

func ptr(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func exec(q, args string) int64 {
	sp, sl := ptr([]byte(q))
	ap, al := ptr([]byte(args))
	return rawDBExec(sp, sl, ap, al)
}

func query(q, args string) (int32, string) {
	buf := make([]byte, 256<<10)
	sp, sl := ptr([]byte(q))
	ap, al := ptr([]byte(args))
	bp, bc := ptr(buf)
	n := rawDBQuery(sp, sl, ap, al, bp, bc)
	if n <= 0 || n > int32(len(buf)) {
		return n, ""
	}
	return n, string(buf[:n])
}

func lastError() string {
	buf := make([]byte, 1024)
	bp, bc := ptr(buf)
	n := rawDBError(bp, bc)
	if n <= 0 || n > int32(len(buf)) {
		return ""
	}
	return string(buf[:n])
}

func main() { plugin.Run(plugin.Handlers{"*": handle}) }

func handle(ev plugin.Event) (any, error) {
	var p struct {
		Mode string               `json:"mode"`
		SQL  map[string][2]string `json:"sql"`
	}
	_ = ev.Decode(&p)
	res := map[string]any{}
	run := func(name string) int64 { return exec(p.SQL[name][0], p.SQL[name][1]) }
	ask := func(name string) (int32, string) { return query(p.SQL[name][0], p.SQL[name][1]) }
	switch p.Mode {
	case "write":
		for _, name := range []string{"create", "insert1", "insert2", "insert3", "update"} {
			res[name] = run(name)
		}
		n, out := ask("read_plain")
		res["query_n"], res["query_out"] = n, out
	case "read":
		n, out := ask("read_blob")
		res["query_n"], res["query_out"] = n, out
	case "attach":
		res["attach"] = exec(`ATTACH DATABASE '/tmp/escape.db' AS o`, "")
		res["attach_err"] = lastError()
		res["vacuum"] = exec(`VACUUM INTO '/tmp/escape2.db'`, "")
		res["pragma"] = exec(`PRAGMA max_page_count=9999999`, "")
		res["multi"] = run("multi")
	case "bad":
		res["syntax"] = run("syntax")
		res["syntax_err"] = lastError()
	}
	b, _ := json.Marshal(res)
	if err := plugin.StorageSet("results", b); err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
