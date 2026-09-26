package cloudlink

// Test-only accessors (kept out of the build so the whole-program deadcode
// guard sees no test-only production code).

// State reports what the link is doing.
func (c *Client) State() State { return State(c.state.Load()) }
