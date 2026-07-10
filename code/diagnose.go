package main

import (
	"fmt"
	"strings"
)

// diagnose classifies a failed tunwg run from the tail of its log output and
// its exit code, returning a short human-readable reason and a longer hint
// with what to check. It is a pure function so it can be unit-tested against
// captured log fixtures.
//
// The patterns are based on tunwg's real failure output (pinned commit in
// reproducible.env). The canonical relay-unreachable case is a single line:
//
//	failed to connect: Post "https://l.tunwg.com/add": dial tcp: lookup l.tunwg.com: i/o timeout
//
// followed by exit status 1 (tunwg log.Fatalf's when it cannot register with
// the relay).
func diagnose(logTail string, exitCode int) (reason, hint string) {
	l := strings.ToLower(logTail)
	has := func(pats ...string) bool {
		for _, p := range pats {
			if strings.Contains(l, p) {
				return true
			}
		}
		return false
	}

	switch {
	// The supervisor could not exec tunwg at all. Keep this ahead of the
	// generic filesystem case: exec errors also contain "permission denied"
	// or "no such file or directory", but the remedy is the image/binary.
	case has("fork/exec", "exec format error"):
		return "Couldn't start the tunwg binary",
			"The plugin container could not execute /usr/local/bin/tunwg. Rebuild or reinstall the plugin image and check that the binary exists, is executable, and matches the container architecture."

	case has("invalid value for --limit"):
		return "Basic auth configuration is invalid",
			"tunwg rejected the value passed to --limit. Re-create this forward with a valid basic-auth username and password."

	// Relay rejected the registration: the tunwg server answers /add with
	// 403 when TUNWG_AUTH does not match, 400 for a malformed key
	// ("error adding peer: <status>"), and the client exits.
	case has("error adding peer", "unexpected relay status", "unauthorized", "forbidden"):
		return "Relay rejected the key or auth token",
			"The relay refused to register this tunnel. If you use a self-hosted relay, replace the relay auth token under Relay settings (TUNWG_AUTH). If the key itself was rejected, delete and re-create the forward to generate a fresh key."

	// DNS lookup, dial or i/o timeout while calling the relay's /add API
	// (l.tunwg.com or a custom TUNWG_API domain).
	case has("no such host", "name resolution", "server misbehaving",
		"temporary failure in name resolution", ": lookup ", "i/o timeout", "connection refused", "no route to host",
		"network is unreachable", "connection reset", "dial tcp",
		"handshake timeout", "deadline exceeded", "connection timed out"):
		return "Can't reach the tunwg relay",
			"The plugin container could not reach the relay over the WAN. Check the router's internet connectivity and DNS from the plugin container, and if you set a custom relay domain under Relay settings, check it for typos (default: l.tunwg.com)."

	// TLS problems talking to the relay (bad certificate on a self-hosted
	// relay, HTTPS-intercepting middlebox, clock far off).
	case has("x509:", "tls:", "certificate"):
		return "TLS problem talking to the relay",
			"The HTTPS connection to the relay failed TLS verification. If you self-host the relay, check its certificate (expired, or issued for a different hostname). An HTTPS-intercepting middlebox or a badly wrong system clock can also cause this."

	// A listen port inside the container is already taken (e.g. a stray
	// TUNWG_PORT or a second process bound to the same port).
	case has("address already in use", "bind:"):
		return "Port conflict inside the plugin container",
			"tunwg could not bind a local port because it is already in use. Restart the plugin container; if it persists, another process in the container holds the port."

	// tunwg cannot read/write its state directory (TUNWG_PATH): WireGuard
	// keys under keys/, ACME certs under certs/.
	case has("permission denied", "read-only file system", "not a directory",
		"no space left on device", "no such file or directory"):
		return "Can't write the tunnel state directory",
			"tunwg failed to read or write its state directory (TUNWG_PATH, /state/plugins/spr-tunwg/tunwg). Check that the plugin's state volume is mounted, writable and not full."

	default:
		return fmt.Sprintf("tunwg exited before publishing a URL (status %d)", exitCode),
			"tunwg reported a startup failure that the plugin does not recognize yet. Open the technical details for its last output; also check the relay domain, WAN/DNS connectivity, and that the plugin state volume is writable."
	}
}
