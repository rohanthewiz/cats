package detect

import (
	"embed"
	"encoding/json"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

//go:embed manifests/*.json
var manifestFS embed.FS

// State is an agent's detected state.
type State string

const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateBlocked State = "blocked"
	StateUnknown State = "unknown"
)

// Input is the screen + OSC context a detection runs against. Screen is the
// terminal tail with rows joined by '\n' (no trailing newline, no '\r').
type Input struct {
	Screen      string
	OscTitle    string
	OscProgress string
}

// Detection is the result of evaluating an agent's manifest against an Input.
type Detection struct {
	State           State
	VisibleIdle     bool
	VisibleBlocker  bool
	VisibleWorking  bool
	SkipStateUpdate bool
}

// --- raw manifest types, mirroring cats's TOML schema ---
//
// The same structs decode both the embedded JSON manifests and the remote TOML
// manifests fetched from the herdr.dev catalog (WS5) — the schemas are 1:1 (the
// JSON files are converted from the Rust TOML sources), so each field carries
// both tags.

type rawManifest struct {
	ID               string    `json:"id" toml:"id"`
	Version          string    `json:"version" toml:"version"`
	MinEngineVersion int       `json:"min_engine_version" toml:"min_engine_version"`
	UpdatedAt        string    `json:"updated_at" toml:"updated_at"`
	Aliases          []string  `json:"aliases" toml:"aliases"`
	Rules            []rawRule `json:"rules" toml:"rules"`
}

type rawRule struct {
	ID              string `json:"id" toml:"id"`
	State           string `json:"state" toml:"state"`
	Priority        int    `json:"priority" toml:"priority"`
	Region          string `json:"region" toml:"region"`
	VisibleIdle     bool   `json:"visible_idle" toml:"visible_idle"`
	VisibleBlocker  bool   `json:"visible_blocker" toml:"visible_blocker"`
	VisibleWorking  bool   `json:"visible_working" toml:"visible_working"`
	SkipStateUpdate bool   `json:"skip_state_update" toml:"skip_state_update"`
	rawGate
}

type rawGate struct {
	All       []rawGate `json:"all" toml:"all"`
	Any       []rawGate `json:"any" toml:"any"`
	Not       []rawGate `json:"not" toml:"not"`
	Contains  []string  `json:"contains" toml:"contains"`
	Regex     []string  `json:"regex" toml:"regex"`
	LineRegex []string  `json:"line_regex" toml:"line_regex"`
}

// --- compiled types ---

type compiledGate struct {
	all, anyOf, not []compiledGate
	contains        []string // pre-lowercased
	regex           []*regexp.Regexp
	lineRegex       []*regexp.Regexp
}

type compiledRule struct {
	gate            compiledGate
	state           State
	region          string
	visibleIdle     bool
	visibleBlocker  bool
	visibleWorking  bool
	skipStateUpdate bool
	priority        int
}

type compiledManifest struct {
	rules []compiledRule
}

// The manifest store: embedded manifests overlaid with any committed remote
// manifests (WS5). Rebuilt lazily after SetRemoteManifestDir/Reload invalidate
// it; Detect only takes the read lock on the hot path.
var (
	manifestMu sync.RWMutex
	manifests  map[string]*compiledManifest // nil ⇒ rebuild on next use
	remoteDir  string                       // "" ⇒ embedded only
)

// SetRemoteManifestDir points detection at the agent-detection state root
// (containing remote/<agent>.toml overlays committed by the updater) and
// invalidates the store. Call once at startup before detection begins.
func SetRemoteManifestDir(dir string) {
	manifestMu.Lock()
	remoteDir = dir
	manifests = nil
	manifestMu.Unlock()
}

// Reload invalidates the manifest store so the next detection rebuilds it —
// called by the updater after committing new remote manifests.
func Reload() {
	manifestMu.Lock()
	manifests = nil
	manifestMu.Unlock()
}

// ensureManifests returns the current store, rebuilding it if invalidated.
func ensureManifests() map[string]*compiledManifest {
	manifestMu.RLock()
	m := manifests
	manifestMu.RUnlock()
	if m != nil {
		return m
	}
	manifestMu.Lock()
	defer manifestMu.Unlock()
	if manifests == nil {
		manifests = loadManifests(remoteDir)
	}
	return manifests
}

// loadManifests builds the store: every embedded manifest, each replaced by its
// committed remote overlay when one parses and passes validation. A broken
// remote file falls back to the bundled manifest — never a missing agent.
func loadManifests(remoteRoot string) map[string]*compiledManifest {
	m := make(map[string]*compiledManifest)
	entries, err := manifestFS.ReadDir("manifests")
	if err != nil {
		return m
	}
	for _, e := range entries {
		data, err := manifestFS.ReadFile("manifests/" + e.Name())
		if err != nil {
			continue
		}
		var rm rawManifest
		if err := json.Unmarshal(data, &rm); err != nil {
			continue
		}
		cm, err := compileManifest(&rm)
		if err != nil || rm.ID == "" {
			continue
		}
		m[rm.ID] = cm
	}
	if remoteRoot == "" {
		return m
	}
	for id := range m {
		data, err := os.ReadFile(remoteManifestPath(remoteRoot, id))
		if err != nil {
			continue // no remote overlay for this agent
		}
		rm, err := parseRemoteManifest(id, data)
		if err != nil {
			log.Printf("detect: ignoring remote manifest for %s: %v", id, err)
			continue
		}
		cm, err := compileManifest(rm)
		if err != nil {
			log.Printf("detect: ignoring remote manifest for %s: %v", id, err)
			continue
		}
		m[id] = cm
	}
	return m
}

func compileManifest(rm *rawManifest) (*compiledManifest, error) {
	cm := &compiledManifest{}
	for _, r := range rm.Rules {
		gate, err := compileGate(r.rawGate)
		if err != nil {
			return nil, err
		}
		region := strings.TrimSpace(r.Region)
		if region == "" {
			region = "whole_recent" // matches cats's default_region
		}
		cm.rules = append(cm.rules, compiledRule{
			gate:            gate,
			state:           parseState(r.State),
			region:          region,
			visibleIdle:     r.VisibleIdle,
			visibleBlocker:  r.VisibleBlocker,
			visibleWorking:  r.VisibleWorking,
			skipStateUpdate: r.SkipStateUpdate,
			priority:        r.Priority,
		})
	}
	return cm, nil
}

func parseState(s string) State {
	switch s {
	case "idle":
		return StateIdle
	case "working":
		return StateWorking
	case "blocked":
		return StateBlocked
	default:
		return StateUnknown
	}
}

func compileGate(g rawGate) (compiledGate, error) {
	var cg compiledGate
	for _, sub := range g.All {
		c, err := compileGate(sub)
		if err != nil {
			return cg, err
		}
		cg.all = append(cg.all, c)
	}
	for _, sub := range g.Any {
		c, err := compileGate(sub)
		if err != nil {
			return cg, err
		}
		cg.anyOf = append(cg.anyOf, c)
	}
	for _, sub := range g.Not {
		c, err := compileGate(sub)
		if err != nil {
			return cg, err
		}
		cg.not = append(cg.not, c)
	}
	for _, s := range g.Contains {
		cg.contains = append(cg.contains, strings.ToLower(s))
	}
	for _, p := range g.Regex {
		re, err := regexp.Compile(translatePattern(p))
		if err != nil {
			return cg, err
		}
		cg.regex = append(cg.regex, re)
	}
	for _, p := range g.LineRegex {
		re, err := regexp.Compile(translatePattern(p))
		if err != nil {
			return cg, err
		}
		cg.lineRegex = append(cg.lineRegex, re)
	}
	return cg, nil
}

// translatePattern rewrites the Rust `regex` constructs the manifests use into
// Go RE2 equivalents. The two engines are RE2 at heart, so only a few spellings
// differ:
//
//	\uXXXX  \u{H…}  \UXXXXXXXX  \U{H…}  ->  \x{H…}  (Go's only codepoint escape)
//	\p{Alphabetic}                      ->  \p{L}   (Go has no binary properties)
//
// Missing any of these forms is not cosmetic: compileGate fails, and
// loadManifests then discards an otherwise-valid cached remote manifest and
// silently falls back to the older embedded one — which is how the braced
// \u{fe0e} in the hermes manifest went unused.
//
// The scan is escape-aware, walking the pattern instead of doing a blind
// replace: an escaped backslash is copied as a pair so `\\u{1}` (a literal
// backslash followed by the letter u) is left alone rather than mistaken for a
// codepoint escape.
func translatePattern(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); {
		if p[i] != '\\' || i+1 >= len(p) {
			b.WriteByte(p[i])
			i++
			continue
		}
		marker := p[i+1]
		switch marker {
		case 'u', 'U':
			hex, width, ok := unicodeEscapeDigits(p[i+2:], marker)
			if !ok { // not a codepoint escape after all — pass it through
				b.WriteString(p[i : i+2])
				i += 2
				continue
			}
			b.WriteString(`\x{`)
			b.WriteString(hex)
			b.WriteByte('}')
			i += 2 + width
		default:
			// Consumes `\\` as a unit (so the next byte can't start an escape)
			// and lets every other escape through untouched.
			b.WriteString(p[i : i+2])
			i += 2
		}
	}
	return strings.ReplaceAll(b.String(), `\p{Alphabetic}`, `\p{L}`)
}

// unicodeEscapeDigits reads the hex payload following a `\u` / `\U` marker:
// either a braced run ("{fe0f}") or a fixed-width run — 4 digits for \u, 8 for
// \U, matching Rust's grammar. Returns the digits and the bytes they consumed;
// ok is false for anything that isn't a well-formed payload, leaving the caller
// to emit the escape verbatim rather than corrupting it.
func unicodeEscapeDigits(s string, marker byte) (hex string, width int, ok bool) {
	if strings.HasPrefix(s, "{") {
		end := strings.IndexByte(s, '}')
		if end < 0 || !isHexRun(s[1:end]) {
			return "", 0, false
		}
		return s[1:end], end + 1, true
	}
	n := 4
	if marker == 'U' {
		n = 8
	}
	if len(s) < n || !isHexRun(s[:n]) {
		return "", 0, false
	}
	return s[:n], n, true
}

func isHexRun(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// Detect evaluates the agent's manifest against the input and returns the
// highest-priority matching rule's state + flags, or a fallback: idle for a
// known agent with no matching rule, unknown otherwise. label is a canonical
// agent label ("" → unknown).
func Detect(label string, in Input) Detection {
	if label == "" {
		return Detection{State: StateUnknown}
	}
	cm := ensureManifests()[label]
	if cm == nil {
		return Detection{State: StateIdle} // known agent, no manifest
	}
	var matched *compiledRule
	for i := range cm.rules {
		r := &cm.rules[i]
		text := region(in, r.region)
		if !gateMatches(&r.gate, text, strings.ToLower(text)) {
			continue
		}
		// Higher priority wins; first-seen wins on ties (mirrors cats).
		if matched == nil || r.priority > matched.priority {
			matched = r
		}
	}
	if matched == nil {
		return Detection{State: StateIdle} // known agent, no rule matched
	}
	st := matched.state
	return Detection{
		State:           st,
		VisibleIdle:     matched.visibleIdle && st == StateIdle,
		VisibleBlocker:  matched.visibleBlocker && st == StateBlocked,
		VisibleWorking:  matched.visibleWorking && st == StateWorking,
		SkipStateUpdate: matched.skipStateUpdate,
	}
}

func gateMatches(g *compiledGate, text, lowerText string) bool {
	for _, needle := range g.contains {
		if !strings.Contains(lowerText, needle) {
			return false
		}
	}
	for _, re := range g.regex {
		if !re.MatchString(text) {
			return false
		}
	}
	for _, re := range g.lineRegex {
		if !anyLineMatches(re, text) {
			return false
		}
	}
	for i := range g.all {
		if !gateMatches(&g.all[i], text, lowerText) {
			return false
		}
	}
	if len(g.anyOf) > 0 {
		ok := false
		for i := range g.anyOf {
			if gateMatches(&g.anyOf[i], text, lowerText) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for i := range g.not {
		if gateMatches(&g.not[i], text, lowerText) {
			return false
		}
	}
	return true
}

func anyLineMatches(re *regexp.Regexp, text string) bool {
	for _, line := range lines(text) {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// --- region extraction (ports the subset of cats regions the manifests use) ---

func region(in Input, spec string) string {
	switch spec {
	case "osc_title":
		return in.OscTitle
	case "osc_progress":
		return in.OscProgress
	}
	content := in.Screen
	switch spec {
	case "whole_recent":
		return content
	case "after_last_prompt_marker":
		return afterLastPromptMarker(content)
	case "after_last_horizontal_rule":
		return afterLastHorizontalRule(content)
	case "prompt_box_body":
		return promptBoxBody(content)
	}
	if n, ok := regionCount(spec, "bottom_lines"); ok {
		return bottomLines(content, n)
	}
	if n, ok := regionCount(spec, "bottom_non_empty_lines"); ok {
		return bottomNonEmptyLines(content, n)
	}
	return ""
}

func regionCount(spec, name string) (int, bool) {
	rest, ok := strings.CutPrefix(spec, name)
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutPrefix(rest, "(")
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, ")")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return n, true
}

// lines mirrors Rust str::lines(): split on '\n', strip a trailing '\r' per line,
// and do not yield a final empty element after a trailing newline.
func lines(content string) []string {
	if content == "" {
		return nil
	}
	parts := strings.Split(content, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for i := range parts {
		parts[i] = strings.TrimSuffix(parts[i], "\r")
	}
	return parts
}

func lineStartOffset(content string, ls []string, index int) int {
	if index > len(ls) {
		index = len(ls)
	}
	off := 0
	for i := 0; i < index; i++ {
		off += len(ls[i]) + 1
	}
	if off > len(content) {
		off = len(content)
	}
	return off
}

func sliceFromLineIndex(content string, ls []string, index int) string {
	return content[lineStartOffset(content, ls, index):]
}

func bottomLines(content string, count int) string {
	ls := lines(content)
	start := len(ls) - count
	if start < 0 {
		start = 0
	}
	return sliceFromLineIndex(content, ls, start)
}

func bottomNonEmptyLines(content string, count int) string {
	ls := lines(content)
	seen := 0
	start := -1
	for i := len(ls) - 1; i >= 0; i-- {
		if strings.TrimSpace(ls[i]) == "" {
			continue
		}
		start = i
		seen++
		if seen == count {
			break
		}
	}
	if start < 0 {
		return ""
	}
	return sliceFromLineIndex(content, ls, start)
}

func afterLastPromptMarker(content string) string {
	ls := lines(content)
	idx := -1
	for i := len(ls) - 1; i >= 0; i-- {
		if codexPromptLine(ls[i]) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return content
	}
	return sliceFromLineIndex(content, ls, idx+1)
}

func afterLastHorizontalRule(content string) string {
	lastRuleEnd := 0
	offset := 0
	for _, line := range lines(content) {
		next := offset + len(line) + 1
		if isHorizontalRule(line) {
			lastRuleEnd = next
			if lastRuleEnd > len(content) {
				lastRuleEnd = len(content)
			}
		}
		offset = next
	}
	return content[lastRuleEnd:]
}

func promptBoxBody(content string) string {
	ls := lines(content)
	top, ok := promptBoxTopBorderIndex(ls)
	if !ok {
		return ""
	}
	start := lineStartOffset(content, ls, top+1)
	endIndex := len(ls)
	for i := top + 1; i < len(ls); i++ {
		if isHorizontalRule(ls[i]) {
			endIndex = i
			break
		}
	}
	end := lineStartOffset(content, ls, endIndex)
	if start > len(content) {
		start = len(content)
	}
	if end > len(content) {
		end = len(content)
	}
	if start > end {
		return ""
	}
	return content[start:end]
}

func promptBoxTopBorderIndex(ls []string) (int, bool) {
	count := 0
	for i := len(ls) - 1; i >= 0; i-- {
		if isHorizontalRule(ls[i]) {
			count++
			if count == 2 {
				return i, true
			}
		}
	}
	return 0, false
}

func codexPromptLine(line string) bool {
	return line == "›" || strings.HasPrefix(line, "› ")
}

func isHorizontalRule(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	ruleChars := 0
	byteIdx := 0
	for _, r := range t {
		if r != '─' {
			break
		}
		ruleChars++
		byteIdx += len(string(r))
	}
	if ruleChars == 0 {
		return false
	}
	suffix := strings.TrimLeftFunc(t[byteIdx:], unicode.IsSpace)
	return suffix == "" || ruleChars >= 3
}
