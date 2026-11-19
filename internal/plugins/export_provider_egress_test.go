package plugins

import (
	"crypto/tls"
	"crypto/x509"
)

// SetTestEgressForTest points w's http_request egress at a stubbed network
// (ut-docs#6627): names resolve through dns, a dial to any IP in route lands
// on that local listener, and TLS is verified against roots. Everything else
// is the production path: the net:<host> permission check, the public-IP
// rule and the egressDialer checks. Compiled only into test binaries, so the
// external bundled-plugin egress test can reach the unexported seam.
func SetTestEgressForTest(w *WasmRuntime, dns map[string][]string, route map[string]string, roots *x509.CertPool) {
	env := &egressEnv{dns: dns, route: route}
	w.httpClient = newPluginHTTPClient(env.dialer(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
}
