// okc-sim runs a simulated Turkish cash register (YN ÖKC) on a TCP port so
// a till with ut-plugin-tax-tr installed (driver "bridge") can take real
// "pay on the device" tenders before a certified device is in hand.
//
//	go run ./scripts/okc-sim                       # 127.0.0.1:4711
//	go run ./scripts/okc-sim -listen 0.0.0.0:4711  # reachable from a tablet on the LAN
//	go run ./scripts/okc-sim -decline              # every sale refused (tests fail-closed)
//	go run ./scripts/okc-sim -silent               # accepts, never answers (tests the timeout)
//
// Test support only (CLAUDE.md: scripts/ tooling is not domain code).
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/universaltill/universal-till/plugins/tax-tr/okc/sim"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:4711", "address to listen on")
	serial := flag.String("serial", "SIM-0001", "device serial reported in every receipt")
	maker := flag.String("maker", "sim", "maker name reported in every receipt")
	z := flag.Int64("z", 1, "starting Z (daily report) counter")
	decline := flag.Bool("decline", false, "refuse every sale and refund")
	silent := flag.Bool("silent", false, "accept connections but never answer")
	delay := flag.Duration("delay", 0, "delay before every answer, e.g. 1500ms")
	flag.Parse()

	s, err := sim.Start(*listen, sim.Options{Serial: *serial, Maker: *maker, ZNo: *z, DeclineAll: *decline, Silent: *silent, Delay: *delay})
	if err != nil {
		log.Fatalf("okc-sim: %v", err)
	}
	log.Printf("okc-sim: simulated YN ÖKC %s (%s) listening on %s — Z=%d decline=%v silent=%v delay=%s",
		*serial, *maker, s.Addr(), *z, *decline, *silent, *delay)
	log.Printf("okc-sim: point the plugin at it: okc.driver=bridge okc.host=<this machine> okc.port=%d", s.Port())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	_ = s.Close()
	time.Sleep(50 * time.Millisecond)
	log.Printf("okc-sim: stopped; printed %d receipt(s)", len(s.Log()))
}
