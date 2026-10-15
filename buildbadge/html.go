package buildbadge

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"strings"
)

// The badge's front end is kept as ordinary .js/.css files (so an editor lints
// and highlights them) and embedded, so a host project needs no asset pipeline
// and no extra route: everything can ride inline in the page it already serves.
//
// Both are exported raw for hosts that bundle their own assets; Head wraps them
// in tags for hosts that don't.

// JS is the browser library: it defines window.BuildBadge and auto-mounts a
// badge into every element carrying a data-build-badge attribute.
//
//go:embed buildbadge.js
var JS string

// CSS styles the badge (.build-badge) and its default hover card (.bb-tip).
// Every colour is a custom property with a fallback, so a host re-themes it by
// setting --bb-* on :root rather than by overriding selectors.
//
//go:embed buildbadge.css
var CSS string

// DefaultGlobal is the window property the JS reads when mount is not handed
// an info object explicitly.
const DefaultGlobal = "__buildInfo"

// jsIdent is the shape a window property name must have to be spliced into
// "window.<name>=" unquoted. Anything else is refused rather than escaped: the
// name is a compile-time choice of the host, so a bad one is a bug to surface,
// not input to sanitise.
var jsIdent = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// DataScript returns a <script> that publishes this binary's Info as
// window.<global>, for the page's <head>. It returns "" when the hash is
// unknown (built outside a git checkout, and not stamped) or global is not a
// plain JS identifier — the JS then mounts nothing, which is the intended
// "no badge" state rather than a badge reading "unknown".
//
// The value rides through json.Marshal, whose default HTML escaping turns '<'
// into <, so a commit subject containing "</script>" cannot close the tag.
func DataScript(global string) string {
	bi := Get()
	if bi.Hash == "" || !jsIdent.MatchString(global) {
		return ""
	}
	data, err := json.Marshal(bi)
	if err != nil {
		return ""
	}
	return "<script data-buildbadge>window." + global + "=" + string(data) + ";</script>\n"
}

// Head returns everything a page needs for the drop-in badge, for its <head>:
// the stylesheet, the data under DefaultGlobal, and the library. Place
//
//	<small data-build-badge></small>
//
// wherever the badge should appear; the library fills it once the DOM is
// parsed. Like DataScript, it returns "" for a build with no known hash, so an
// unstamped dev binary renders the page without a badge instead of an empty
// one.
func Head() string {
	data := DataScript(DefaultGlobal)
	if data == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<style data-buildbadge>\n")
	b.WriteString(CSS)
	b.WriteString("</style>\n")
	b.WriteString(data)
	b.WriteString(ScriptTag())
	return b.String()
}

// ScriptTag is the library alone, wrapped in a <script>. It is for hosts that
// draw their own hover card and so want neither the stylesheet nor the default
// data global (cats itself is one: it publishes under its own name and routes
// the card through its sidebar's shared popup).
func ScriptTag() string {
	return "<script data-buildbadge>\n" + JS + "</script>\n"
}
