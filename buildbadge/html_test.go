package buildbadge

import (
	"encoding/base64"
	"strings"
	"testing"
)

// DataScript publishes the stamped identity under the requested global, as a
// JSON object a page can read before its own script runs.
func TestDataScriptPublishesInfo(t *testing.T) {
	stampWhen(t, "abc1234", base64.StdEncoding.EncodeToString([]byte("fix it")), "2026-10-15T09:41:07-05:00")
	got := DataScript("__myBuild")
	want := `<script data-buildbadge>window.__myBuild={"hash":"abc1234","subject":"fix it","when":"2026-10-15T14:41:07Z","dirty":`
	if !strings.HasPrefix(got, want) {
		t.Errorf("DataScript =\n %s\nwant prefix\n %s", got, want)
	}
}

// A commit subject holding markup cannot close the <script> it rides in —
// json.Marshal's HTML escaping keeps the '<' inert.
func TestDataScriptEscapesSubject(t *testing.T) {
	stamp(t, "abc1234", base64.StdEncoding.EncodeToString([]byte("</script><script>alert(1)</script>")))
	if got := DataScript(DefaultGlobal); strings.Contains(got, "</script><script>alert(1)") {
		t.Errorf("subject broke out of <script>: %s", got)
	}
}

// The global is spliced into JS unquoted, so anything but a plain identifier is
// refused outright rather than emitted.
func TestDataScriptRejectsBadGlobal(t *testing.T) {
	stamp(t, "abc1234", "")
	for _, g := range []string{"", "a.b", "x;alert(1)", "1abc", "a b"} {
		if got := DataScript(g); got != "" {
			t.Errorf("DataScript(%q) = %q, want empty", g, got)
		}
	}
}

// Head carries all three parts for the drop-in case: styles, data under the
// default global, and the library.
func TestHeadCarriesAllParts(t *testing.T) {
	stamp(t, "abc1234", "")
	h := Head()
	for _, want := range []string{".bb-tip", "window." + DefaultGlobal + "=", "root.BuildBadge = {"} {
		if !strings.Contains(h, want) {
			t.Errorf("Head() missing %q", want)
		}
	}
}

// The embedded assets go out inside <script>/<style> tags, so neither may hold
// the closing tag that would cut it short.
func TestAssetsDoNotCloseTheirTags(t *testing.T) {
	if strings.Contains(strings.ToLower(JS), "</script") {
		t.Error("buildbadge.js contains </script")
	}
	if strings.Contains(strings.ToLower(CSS), "</style") {
		t.Error("buildbadge.css contains </style")
	}
}

// String drops unknown parts instead of leaving empty separators.
func TestInfoString(t *testing.T) {
	for _, c := range []struct {
		in   Info
		want string
	}{
		{Info{Hash: "abc1234", Subject: "fix it", When: "2026-10-15T14:41:07Z", Dirty: true}, "abc1234* · fix it · 2026-10-15T14:41:07Z"},
		{Info{Hash: "abc1234"}, "abc1234"},
		{Info{}, "unknown"},
	} {
		if got := c.in.String(); got != c.want {
			t.Errorf("%+v.String() = %q, want %q", c.in, got, c.want)
		}
	}
}
