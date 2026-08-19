package main

import "testing"

// The scp notation, and the two ways it is ambiguous. A leading "/", "." or "~"
// settles it before the colon is even looked at; otherwise the text before the
// first colon has to look like a cats host id.
func TestParseCPPath(t *testing.T) {
	cases := []struct {
		in         string
		host, path string
		local      bool
	}{
		{"devbox:/var/log/x", "devbox", "/var/log/x", false},
		{"devbox:~/work/x", "devbox", "~/work/x", false},
		{"build-2.eu:notes.md", "build-2.eu", "notes.md", false},
		// "local" is an ordinary host id (config.LocalHostID), so it goes the
		// same way as any other: through catway, to the machine it names.
		{"local:x", "local", "x", false},

		{"./x", "", "./x", true},
		{"/var/log/x", "", "/var/log/x", true},
		{"~/x", "", "~/x", true},
		{"x", "", "x", true},
		{".", "", ".", true},

		// The ambiguous ones a naive split would get wrong.
		{"./weird:name", "", "./weird:name", true},
		{"/var/log/a:b", "", "/var/log/a:b", true},
		{":leading", "", ":leading", true},
		{"has space:x", "", "has space:x", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := parseCPPath(c.in)
			if got.local != c.local || got.host != c.host || got.path != c.path {
				t.Errorf("parseCPPath(%q) = {host:%q path:%q local:%v}, want {host:%q path:%q local:%v}",
					c.in, got.host, got.path, got.local, c.host, c.path, c.local)
			}
		})
	}
}

// The rendered form is what error messages and the completion line show, so it
// has to be the notation the user typed rather than a normalized one.
func TestCPPathString(t *testing.T) {
	if got := parseCPPath("devbox:/a/b").String(); got != "devbox:/a/b" {
		t.Errorf("String() = %q", got)
	}
	if got := parseCPPath("./a").String(); got != "./a" {
		t.Errorf("String() = %q", got)
	}
}
