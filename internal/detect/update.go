// Remote manifest updates (WS5) — the port of herdr's manifest_update.rs.
//
// A TOML catalog hosted at herdr.dev lists per-agent manifest files; the
// updater fetches it, applies each agent's manifest that is strictly newer than
// the cached one (downgrades and same-version content changes are rejected as
// tampering), commits accepted manifests atomically under
// <stateDir>/remote/<agent>.toml, and records the outcome in
// <stateDir>/status.json. The committed overlay is what loadManifests layers
// over the embedded set. The remote/*.toml layout and format match the Rust
// implementation, so a machine running both shares the cache; the status file
// is JSON (Go writes no TOML) and lives alongside Rust's status.toml.
package detect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

// EngineVersion is the rules-engine version this package implements (herdr's
// MANIFEST_ENGINE_VERSION). A remote manifest whose min_engine_version exceeds
// it is refused — it may use constructs this engine cannot evaluate.
const EngineVersion = 2

const (
	defaultCatalogURL = "https://herdr.dev/agent-detection/index.toml"
	// CatalogURLEnv overrides the catalog location (mirrors the Rust env var).
	CatalogURLEnv = "HERDR_AGENT_DETECTION_MANIFEST_CATALOG_URL"
	maxFetchBytes = 256 << 10
)

// Remote-manifest complexity limits (ported from manifest.rs): a fetched
// manifest is untrusted input, so its size is bounded before compilation.
const (
	maxRulesPerManifest = 128
	maxGateDepth        = 8
	maxTotalGates       = 512
	maxMatchersPerGate  = 32
	maxTotalMatchers    = 1024
	maxMatcherChars     = 512
)

// UpdateCommit records one agent's manifest being updated to a version.
type UpdateCommit struct {
	Agent   string
	Version string
}

// AgentRemoteStatus is one agent's slice of the update status file.
type AgentRemoteStatus struct {
	CachedVersion    string `json:"cached_version,omitempty"`
	AttemptedVersion string `json:"attempted_version,omitempty"`
	LastCheckedUnix  int64  `json:"last_checked_unix,omitempty"`
	LastResult       string `json:"last_result"` // updated | current | failed
	LastError        string `json:"last_error,omitempty"`
}

// UpdateStatus is the persisted outcome of the last update run (status.json).
type UpdateStatus struct {
	LastCheckUnix int64                        `json:"last_check_unix,omitempty"`
	LastResult    string                       `json:"last_result,omitempty"`
	Agents        map[string]AgentRemoteStatus `json:"agents"`
}

// UpdateOutput is a completed update run: what changed, and the full status.
type UpdateOutput struct {
	Updated []UpdateCommit
	Status  UpdateStatus
}

// AutoUpdate runs one background update pass against the configured catalog
// (env override, else the default), reloads the manifest store when anything
// changed, and logs the outcome. Meant to be launched in a goroutine at daemon
// startup; every failure mode degrades to the manifests already on disk.
func AutoUpdate(stateDir string) {
	out, err := CheckAndUpdate(stateDir, CatalogURL())
	if err != nil {
		log.Printf("detect: manifest update failed: %v", err)
		st := loadStatus(stateDir)
		st.LastCheckUnix = time.Now().Unix()
		st.LastResult = "failed: " + err.Error()
		if err := saveStatus(stateDir, st); err != nil {
			log.Printf("detect: save manifest update status: %v", err)
		}
		return
	}
	if len(out.Updated) > 0 {
		Reload()
		for _, c := range out.Updated {
			log.Printf("detect: agent manifest updated: %s → %s", c.Agent, c.Version)
		}
	} else {
		log.Printf("detect: agent manifests up to date")
	}
}

// CatalogURL resolves the catalog location: env override, else the default.
func CatalogURL() string {
	if v := strings.TrimSpace(os.Getenv(CatalogURLEnv)); v != "" {
		return v
	}
	return defaultCatalogURL
}

// CheckAndUpdate fetches the catalog and processes every listed agent,
// committing newer manifests and recording per-agent outcomes. Per-agent
// failures are recorded, not fatal; only an unusable catalog is an error.
func CheckAndUpdate(stateDir, url string) (UpdateOutput, error) {
	body, err := fetchText(url)
	if err != nil {
		return UpdateOutput{}, err
	}
	catalog, err := parseCatalog(body)
	if err != nil {
		return UpdateOutput{}, err
	}
	base, err := baseURL(url)
	if err != nil {
		return UpdateOutput{}, err
	}

	status := loadStatus(stateDir)
	checkTime := time.Now().Unix()
	status.LastCheckUnix = checkTime
	status.LastResult = "checked"

	var updated []UpdateCommit
	for _, entry := range catalog {
		manifestURL, err := joinURL(base, entry.path)
		var commit *UpdateCommit
		if err == nil {
			var content string
			if content, err = fetchText(manifestURL); err != nil {
				err = fmt.Errorf("fetch failed: %w", err)
			} else {
				commit, err = processAgentManifest(stateDir, entry.id, content)
			}
		}
		switch {
		case err != nil:
			log.Printf("detect: manifest update failed for %s: %v", entry.id, err)
			status.Agents[entry.id] = AgentRemoteStatus{
				CachedVersion:   cachedRemoteVersionString(stateDir, entry.id),
				LastCheckedUnix: checkTime,
				LastResult:      "failed",
				LastError:       err.Error(),
			}
		case commit != nil:
			status.Agents[entry.id] = AgentRemoteStatus{
				CachedVersion:    commit.Version,
				AttemptedVersion: commit.Version,
				LastCheckedUnix:  checkTime,
				LastResult:       "updated",
			}
			updated = append(updated, *commit)
		default:
			status.Agents[entry.id] = AgentRemoteStatus{
				CachedVersion:   cachedRemoteVersionString(stateDir, entry.id),
				LastCheckedUnix: checkTime,
				LastResult:      "current",
			}
		}
	}

	if err := saveStatus(stateDir, status); err != nil {
		log.Printf("detect: save manifest update status: %v", err)
		status.LastResult = "failed_to_save_status: " + err.Error()
	}
	return UpdateOutput{Updated: updated, Status: status}, nil
}

// processAgentManifest validates a fetched manifest against the cached one:
// strictly newer ⇒ commit; same version + same content ⇒ no-op; a downgrade or
// a same-version content change ⇒ rejected (tampering). Returns the commit made
// (nil for a no-op).
func processAgentManifest(stateDir, id, content string) (*UpdateCommit, error) {
	parsed, err := parseRemoteManifest(id, []byte(content))
	if err != nil {
		return nil, err
	}
	version, err := parseManifestVersion(parsed.Version)
	if err != nil {
		return nil, err
	}
	if current, ok := cachedRemoteVersion(stateDir, id); ok {
		switch compareManifestVersions(version, current) {
		case -1:
			return nil, fmt.Errorf("remote version %s is older than cached %s",
				parsed.Version, versionString(current))
		case 0:
			committed, _ := os.ReadFile(remoteManifestPath(stateDir, id))
			if string(committed) != content {
				return nil, fmt.Errorf("remote version %s changed content without a version bump", parsed.Version)
			}
			return nil, nil
		}
	}
	if err := atomicWriteFile(remoteManifestPath(stateDir, id), []byte(content)); err != nil {
		return nil, err
	}
	return &UpdateCommit{Agent: id, Version: parsed.Version}, nil
}

// parseRemoteManifest strictly decodes an untrusted remote TOML manifest and
// validates it: id must match the expected agent, version and a compatible
// min_engine_version are required, and the complexity limits hold. Mirrors
// parse_remote_manifest_for_agent + validate_manifest.
func parseRemoteManifest(id string, data []byte) (*rawManifest, error) {
	var rm rawManifest
	dec := toml.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rm); err != nil {
		return nil, fmt.Errorf("parse manifest TOML: %w", err)
	}
	if rm.ID != id {
		return nil, fmt.Errorf("manifest id %q does not match %q", rm.ID, id)
	}
	if strings.TrimSpace(rm.Version) == "" {
		return nil, errors.New("remote manifest must include version")
	}
	if _, err := parseManifestVersion(rm.Version); err != nil {
		return nil, err
	}
	if rm.MinEngineVersion == 0 {
		return nil, errors.New("remote manifest must include min_engine_version")
	}
	if rm.MinEngineVersion > EngineVersion {
		return nil, fmt.Errorf("manifest requires engine %d, current engine is %d",
			rm.MinEngineVersion, EngineVersion)
	}
	if err := validateManifestLimits(&rm); err != nil {
		return nil, err
	}
	return &rm, nil
}

// validateManifestLimits bounds an untrusted manifest's size and nesting.
func validateManifestLimits(rm *rawManifest) error {
	if len(rm.Rules) == 0 {
		return errors.New("manifest must contain at least one rule")
	}
	if len(rm.Rules) > maxRulesPerManifest {
		return fmt.Errorf("manifest contains %d rules, max is %d", len(rm.Rules), maxRulesPerManifest)
	}
	gates, matchers := 0, 0
	for i := range rm.Rules {
		if err := validateGateLimits(&rm.Rules[i].rawGate, 1, &gates, &matchers); err != nil {
			return err
		}
	}
	return nil
}

func validateGateLimits(g *rawGate, depth int, gates, matchers *int) error {
	if depth > maxGateDepth {
		return fmt.Errorf("gate nesting exceeds depth %d", maxGateDepth)
	}
	if *gates++; *gates > maxTotalGates {
		return fmt.Errorf("manifest exceeds %d gates", maxTotalGates)
	}
	own := len(g.Contains) + len(g.Regex) + len(g.LineRegex)
	if own > maxMatchersPerGate {
		return fmt.Errorf("gate has %d matchers, max is %d", own, maxMatchersPerGate)
	}
	if *matchers += own; *matchers > maxTotalMatchers {
		return fmt.Errorf("manifest exceeds %d matchers", maxTotalMatchers)
	}
	for _, list := range [][]string{g.Contains, g.Regex, g.LineRegex} {
		for _, m := range list {
			if len(m) > maxMatcherChars {
				return fmt.Errorf("matcher exceeds %d chars", maxMatcherChars)
			}
		}
	}
	for _, subs := range [][]rawGate{g.All, g.Any, g.Not} {
		for i := range subs {
			if err := validateGateLimits(&subs[i], depth+1, gates, matchers); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- versions (dotted numeric, trailing zeros insignificant) -----------------

// parseManifestVersion parses a dotted-numeric version ("2026.6.10.1") into its
// segments. Empty/non-numeric/oversized segments are rejected.
func parseManifestVersion(value string) ([]uint64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, errors.New("version must not be empty")
	}
	var segs []uint64
	for s := range strings.SplitSeq(trimmed, ".") {
		if s == "" {
			return nil, fmt.Errorf("version %q contains an empty segment", trimmed)
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("version %q must be dotted numeric", trimmed)
		}
		segs = append(segs, n)
	}
	return segs, nil
}

// compareManifestVersions orders two parsed versions; missing segments count as
// zero, so "1.2" == "1.2.0" (mirrors the Rust Ord impl).
func compareManifestVersions(a, b []uint64) int {
	for i := range max(len(a), len(b)) {
		var av, bv uint64
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionString(segs []uint64) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = strconv.FormatUint(s, 10)
	}
	return strings.Join(parts, ".")
}

// cachedRemoteVersion reads the committed remote manifest's version, if any.
func cachedRemoteVersion(stateDir, id string) ([]uint64, bool) {
	data, err := os.ReadFile(remoteManifestPath(stateDir, id))
	if err != nil {
		return nil, false
	}
	rm, err := parseRemoteManifest(id, data)
	if err != nil {
		return nil, false
	}
	v, err := parseManifestVersion(rm.Version)
	if err != nil {
		return nil, false
	}
	return v, true
}

func cachedRemoteVersionString(stateDir, id string) string {
	if v, ok := cachedRemoteVersion(stateDir, id); ok {
		return versionString(v)
	}
	return ""
}

// --- catalog -----------------------------------------------------------------

type catalogEntry struct {
	id   string
	path string
}

type rawCatalog struct {
	SchemaVersion int               `toml:"schema_version"`
	Agents        []rawCatalogAgent `toml:"agents"`
}

type rawCatalogAgent struct {
	ID   string `toml:"id"`
	Path string `toml:"path"`
}

// parseCatalog strictly decodes the catalog TOML and validates it: schema
// version 1, safe relative paths, no duplicates. Agents this build doesn't know
// (no embedded manifest) are skipped with a log — a newer catalog may list
// agents a newer herdr detects.
func parseCatalog(content string) ([]catalogEntry, error) {
	var cat rawCatalog
	dec := toml.NewDecoder(strings.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cat); err != nil {
		return nil, fmt.Errorf("parse catalog TOML: %w", err)
	}
	if cat.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported catalog schema_version %d", cat.SchemaVersion)
	}
	known := ensureManifests()
	seen := make(map[string]bool)
	var entries []catalogEntry
	for _, a := range cat.Agents {
		if known[a.ID] == nil {
			log.Printf("detect: skipping unknown remote manifest agent %q", a.ID)
			continue
		}
		if strings.TrimSpace(a.Path) == "" {
			return nil, fmt.Errorf("catalog entry %s has an empty path", a.ID)
		}
		if err := checkRelPath(a.Path); err != nil {
			return nil, fmt.Errorf("catalog entry %s has an unsafe path %s", a.ID, a.Path)
		}
		if seen[a.ID] {
			return nil, fmt.Errorf("catalog contains duplicate agent %s", a.ID)
		}
		seen[a.ID] = true
		entries = append(entries, catalogEntry{id: a.ID, path: a.Path})
	}
	return entries, nil
}

// checkRelPath rejects absolute, scheme-carrying, or parent-escaping paths.
func checkRelPath(p string) error {
	if strings.Contains(p, "://") || strings.HasPrefix(p, "/") {
		return errors.New("unsafe path")
	}
	if slices.Contains(strings.Split(p, "/"), "..") {
		return errors.New("unsafe path")
	}
	return nil
}

func baseURL(url string) (string, error) {
	i := strings.LastIndex(url, "/")
	if i < 0 {
		return "", fmt.Errorf("catalog URL %s has no base path", url)
	}
	return url[:i], nil
}

func joinURL(base, path string) (string, error) {
	if err := checkRelPath(path); err != nil {
		return "", fmt.Errorf("unsafe manifest path %s", path)
	}
	return strings.TrimRight(base, "/") + "/" + path, nil
}

// --- fetch -------------------------------------------------------------------

// fetchClient is the HTTP client for catalog/manifest fetches — bounded total
// time, as curl's --max-time 15 was in the Rust implementation. Overridable in
// tests.
var fetchClient = &http.Client{Timeout: 15 * time.Second}

// fetchText GETs a small UTF-8 text resource with a size cap and one retry on
// transient failure (curl --retry 2 equivalent, minus curl).
func fetchText(url string) (string, error) {
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		body, err := fetchOnce(url)
		if err == nil {
			return body, nil
		}
		lastErr = err
	}
	return "", lastErr
}

func fetchOnce(url string) (string, error) {
	resp, err := fetchClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch %s: status %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", url, err)
	}
	if len(data) > maxFetchBytes {
		return "", fmt.Errorf("response from %s exceeded %d bytes", url, maxFetchBytes)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("response from %s was not UTF-8", url)
	}
	return string(data), nil
}

// --- state files -------------------------------------------------------------

// remoteManifestPath is where an agent's committed remote manifest lives —
// the same layout as the Rust implementation (shared cache).
func remoteManifestPath(stateDir, id string) string {
	return filepath.Join(stateDir, "remote", id+".toml")
}

func statusPath(stateDir string) string {
	return filepath.Join(stateDir, "status.json")
}

func loadStatus(stateDir string) UpdateStatus {
	st := UpdateStatus{Agents: map[string]AgentRemoteStatus{}}
	data, err := os.ReadFile(statusPath(stateDir))
	if err != nil {
		return st
	}
	if err := json.Unmarshal(data, &st); err != nil {
		log.Printf("detect: unreadable manifest update status: %v", err)
		return UpdateStatus{Agents: map[string]AgentRemoteStatus{}}
	}
	if st.Agents == nil {
		st.Agents = map[string]AgentRemoteStatus{}
	}
	return st
}

func saveStatus(stateDir string, st UpdateStatus) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicWriteFile(statusPath(stateDir), data)
}

// atomicWriteFile writes bytes via a same-dir temp file + fsync + rename.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
