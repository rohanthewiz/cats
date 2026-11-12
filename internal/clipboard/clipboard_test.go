package clipboard

import (
	"os"
	"os/exec"
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

// UTF8Env must win over whatever locale the process inherited, including one
// that is set but not UTF-8. pbpaste falls back to MacRoman when ANY of LANG,
// LC_ALL or LC_CTYPE is non-UTF-8 (see UTF8Env), so a parent with only
// LC_ALL=C would still break the read if just LANG were overridden.
func TestUTF8EnvOverridesInheritedLocale(t *testing.T) {
	t.Setenv("LANG", "C")
	t.Setenv("LC_ALL", "C")
	t.Setenv("LC_CTYPE", "C")

	// os/exec keeps the last value of a duplicated key; resolve the same way.
	last := map[string]string{}
	for _, kv := range UTF8Env() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			last[k] = v
		}
	}
	for _, k := range []string{"LANG", "LC_ALL", "LC_CTYPE"} {
		if !strings.HasSuffix(last[k], ".UTF-8") {
			t.Errorf("%s = %q under UTF8Env, want a UTF-8 locale", k, last[k])
		}
	}
	if last["HOME"] != os.Getenv("HOME") {
		t.Error("UTF8Env dropped the inherited environment")
	}
}

// The real tools, in the environment Cats.app actually runs in: launched by
// launchd, so no locale at all. The bug this guards was smart quotes and emoji
// coming back from pbpaste as MacRoman (and the mac app then reporting
// "clipboard has no text"), and the same text copied OUT of a pane landing on
// the pasteboard as mojibake. It uses the `ruler` pasteboard rather than the
// general one so running the tests never clobbers what the user has copied.
//
// Each direction is paired with a reference side whose locale is spelled out
// here, independently of UTF8Env. Pairing UTF8Env with itself would prove
// nothing: with no locale both tools use MacRoman, and the two mistranslations
// cancel out in a round trip — the bug only shows when one side is the real,
// Unicode-native pasteboard user (any other app).
func TestPasteboardRoundTripsUnicodeWithoutLocale(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("pbcopy/pbpaste are macOS tools")
	}
	if _, err := exec.LookPath("pbpaste"); err != nil {
		t.Skip("pbpaste not on PATH")
	}
	for _, k := range []string{"LANG", "LC_ALL", "LC_CTYPE"} {
		t.Setenv(k, "") // registers the restore …
		os.Unsetenv(k)  // … then removes the key outright, as launchd leaves it
	}
	reference := append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8", "LC_CTYPE=en_US.UTF-8")

	const want = "smart “quotes” — café 🐱"
	copyWith := func(env []string) {
		t.Helper()
		cmd := exec.Command("pbcopy", "-pboard", "ruler")
		cmd.Env = env
		cmd.Stdin = strings.NewReader(want)
		if err := cmd.Run(); err != nil {
			t.Skipf("pbcopy unavailable here (no pasteboard server?): %v", err)
		}
	}
	pasteWith := func(env []string) string {
		t.Helper()
		cmd := exec.Command("pbpaste", "-pboard", "ruler")
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("pbpaste: %v", err)
		}
		return string(out)
	}

	// Read: another app put Unicode on the pasteboard; cats reads it.
	copyWith(reference)
	if got := pasteWith(UTF8Env()); got != want {
		t.Errorf("read under UTF8Env = %q, want %q", got, want)
	}
	// Write: cats puts it there; another app reads it.
	copyWith(UTF8Env())
	if got := pasteWith(reference); got != want {
		t.Errorf("write under UTF8Env, read back = %q, want %q", got, want)
	}
}
