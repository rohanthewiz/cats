//go:build ghostty

package main

import "net"

// serveURL is the address the startup line tells a human to open.
//
// The line used to be "localhost" glued in front of the listen address, which is
// right only for the ":8421" form. catapp passes "127.0.0.1:8422", which came out
// as "http://localhost127.0.0.1:8422" — a URL nobody can paste.
//
//	":8421"            → http://localhost:8421   (all interfaces; localhost reaches it)
//	"0.0.0.0:8421"     → http://localhost:8421   (same, spelled out)
//	"[::]:8421"        → http://localhost:8421
//	"127.0.0.1:8422"   → http://127.0.0.1:8422   (the host it is actually bound to)
//	"[::1]:8421"       → http://[::1]:8421
//
// A wildcard host becomes localhost rather than being printed as-is: 0.0.0.0 is a
// bind address, not a destination, and the person reading this line is almost
// always on the same machine. An address that does not parse is printed after the
// scheme unchanged, so the line never hides what was configured.
func serveURL(scheme, addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return scheme + "://" + addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "localhost"
	}
	// JoinHostPort brackets an IPv6 literal, which a URL needs.
	return scheme + "://" + net.JoinHostPort(host, port)
}
