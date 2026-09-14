//go:build ghostty

package main

import "testing"

func TestServeURL(t *testing.T) {
	cases := []struct {
		scheme, addr, want string
	}{
		{"http", ":8421", "http://localhost:8421"},
		{"http", "0.0.0.0:8421", "http://localhost:8421"},
		{"http", "[::]:8421", "http://localhost:8421"},
		// catapp's form: the one that printed "localhost127.0.0.1:8422".
		{"http", "127.0.0.1:8422", "http://127.0.0.1:8422"},
		{"https", "cats.lan:443", "https://cats.lan:443"},
		{"http", "[::1]:8421", "http://[::1]:8421"},
		// Unparseable: shown as configured rather than dropped.
		{"http", "nonsense", "http://nonsense"},
	}
	for _, c := range cases {
		if got := serveURL(c.scheme, c.addr); got != c.want {
			t.Errorf("serveURL(%q, %q) = %q, want %q", c.scheme, c.addr, got, c.want)
		}
	}
}
