package orchestration

import "testing"

func TestOscScannerOSC7(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"file_localhost_bel", "\x1b]7;file://localhost/tmp\x07", "/tmp"},
		{"file_emptyhost_st", "\x1b]7;file:///var/log\x1b\\", "/var/log"},
		{"bare_path_bel", "\x1b]7;/home/user\x07", "/home/user"},
		{"percent_decoded", "\x1b]7;file://h/a%20b\x07", "/a b"},
		{"surrounded_by_output", "hello\x1b]7;file://h/srv\x07world", "/srv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s oscScanner
			cwd, ok := s.scan([]byte(tc.in))
			if !ok || cwd != tc.want {
				t.Fatalf("scan(%q) = (%q, %v), want (%q, true)", tc.in, cwd, ok, tc.want)
			}
		})
	}
}

func TestOscScannerIgnoresOther(t *testing.T) {
	var s oscScanner
	// OSC 0 (title) and plain text must not yield a cwd.
	if cwd, ok := s.scan([]byte("\x1b]0;a title\x07plain text")); ok {
		t.Fatalf("unexpected cwd %q from non-OSC7 input", cwd)
	}
}

func TestOscScannerSplitAcrossReads(t *testing.T) {
	var s oscScanner
	// An OSC 7 sequence delivered in three chunks (mid-sequence boundaries).
	chunks := []string{"\x1b]7;file://lo", "calhost/usr/lo", "cal\x07"}
	var got string
	var ok bool
	for _, ch := range chunks {
		if c, found := s.scan([]byte(ch)); found {
			got, ok = c, true
		}
	}
	if !ok || got != "/usr/local" {
		t.Fatalf("split scan = (%q, %v), want (/usr/local, true)", got, ok)
	}
}

func TestOscScannerOverlongAbandoned(t *testing.T) {
	var s oscScanner
	big := make([]byte, oscMaxLen+10)
	for i := range big {
		big[i] = 'x'
	}
	in := append([]byte("\x1b]7;file://h"), big...)
	in = append(in, 0x07)
	if cwd, ok := s.scan(in); ok {
		t.Fatalf("overlong OSC should be abandoned, got %q", cwd)
	}
}
