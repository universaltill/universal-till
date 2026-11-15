package pages

// unreachableAddr is a host:port nothing listens on. Port 1 is privileged and
// unused, so a dial is refused straight away: no timeout and no reuse race
// (a closed httptest server's port can be handed to a later server in the
// same package — ut-docs#4996, ut-docs#5019).
func unreachableAddr() string {
	return "127.0.0.1:1"
}

// unreachableURL is the http:// base URL form of unreachableAddr.
func unreachableURL() string {
	return "http://" + unreachableAddr()
}
