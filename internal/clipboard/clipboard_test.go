package clipboard

import (
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

// The cap exists so one enormous paste cannot turn a control response into a
// multi-megabyte line, and the cut has to land on a rune boundary or the JSON
// encoding replaces the fragment with U+FFFD — a character the user never
// copied, silently diffed against by the caller.
func TestTrimPartialRune(t *testing.T) {
	// "héllo☃" — a 2-byte rune and a 3-byte rune, so every truncation depth
	// below exercises a different lead/continuation shape.
	full := []byte("héllo☃")

	for cut := 0; cut <= len(full); cut++ {
		got := trimPartialRune(full[:cut])
		if !utf8.Valid(got) {
			t.Errorf("cut at %d left invalid UTF-8: %q", cut, got)
		}
		if len(got) > cut {
			t.Errorf("cut at %d grew the slice to %d bytes", cut, len(got))
		}
		// Nothing whole may be dropped: the result is the longest valid prefix,
		// so at most 3 bytes (one incomplete 4-byte sequence) can go.
		if cut-len(got) > 3 {
			t.Errorf("cut at %d dropped %d bytes, want at most 3", cut, cut-len(got))
		}
		if !strings.HasPrefix(string(full), string(got)) {
			t.Errorf("cut at %d is not a prefix of the input: %q", cut, got)
		}
	}

	// A truncated 4-byte rune (an emoji) is the widest case, and the one a plain
	// "walk back over continuation bytes" loop gets wrong if it stops at 3.
	emoji := []byte("a🙂")
	for cut := 1; cut < len(emoji); cut++ {
		if got := trimPartialRune(emoji[:cut]); !utf8.Valid(got) {
			t.Errorf("emoji cut at %d left invalid UTF-8: %q", cut, got)
		}
	}
}

// A clipboard tool's stderr can run to a usage block; the message ends up in an
// editor's status bar, so it is kept to one line.
func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"":                     "",
		"no newline":           "no newline",
		"first\nsecond\nthird": "first",
		"  padded  \nrest":     "padded",
		"trailing only\n":      "trailing only",
	}
	for in, want := range cases {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// The candidate order is load-bearing on Linux: a Wayland session commonly has
// Xwayland (and so xclip) installed too, and answering from the X11 clipboard
// there would return whatever was copied in some legacy app rather than what the
// user just copied. Pin the order rather than trusting the slice literal.
func TestReaderOrder(t *testing.T) {
	got := readers()
	switch runtime.GOOS {
	case "darwin":
		if len(got) != 1 || got[0].name != "pbpaste" {
			t.Fatalf("darwin readers = %+v, want [pbpaste]", got)
		}
	case "linux":
		if len(got) < 2 || got[0].name != "wl-paste" || got[1].name != "xclip" {
			t.Fatalf("linux readers = %+v, want wl-paste before xclip", got)
		}
		if !got[0].emptyIsError {
			t.Error("wl-paste must be marked emptyIsError: it exits non-zero on an empty clipboard")
		}
	}
	for _, r := range got {
		if r.name == "" {
			t.Errorf("reader with no program name: %+v", r)
		}
	}
}

// The end-to-end read, on a host that actually has a clipboard tool. It asserts
// only what is true of ANY clipboard state — including an empty one, which must
// come back as ("", nil) rather than an error, since "nothing copied yet" is a
// normal state and not a fault to report.
func TestReadDoesNotFailOnAnyClipboardState(t *testing.T) {
	if !Available() {
		t.Skip("no clipboard reader on this host")
	}
	text, truncated, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(text) > MaxBytes {
		t.Fatalf("Read returned %d bytes, over the %d cap", len(text), MaxBytes)
	}
	if truncated && len(text) == 0 {
		t.Fatal("Read reported truncation with no text")
	}
	if !utf8.ValidString(text) {
		t.Error("Read returned invalid UTF-8")
	}
}
