package config

// The JSON config file: format selection, the one-time YAML → JSON migration,
// and section-preserving writes.
//
// One file, two writers. catway owns every section modelled by Config; the Mac
// app (cmd/catapp) owns a top-level "app" section Config knows nothing about —
// its launch mode, saved catways and window layout. Both do read-modify-write
// under an advisory lock on a sidecar file, and each replaces only what it
// owns:
//
//	              config.json
//	   ┌──────────────────────────────┐
//	   │ "server": {…}                │ ◄── catway: Save(cfg)
//	   │ "panes":  {…}   … "ui": {…}  │     rewrites every Config key,
//	   │                              │     copies unknown keys through
//	   │ "app":    {…}                │ ◄── catapp: WriteSection("app", …)
//	   └──────────────────────────────┘     rewrites that one key only
//
// Without the "copy unknown keys through" rule a settings save in the browser
// would erase the Mac app's saved catways; without the lock, a window move in
// the app racing a settings save could lose either write.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

// DefaultFile is the config filename inside the config dir.
const DefaultFile = "config.json"

// legacyFile is the pre-JSON filename, read once by migrateLegacy.
const legacyFile = "config.yaml"

// isYAMLPath reports whether path names a YAML config. Only the extension is
// consulted — sniffing content would make a file's format depend on its first
// byte, and a half-written JSON file would then "become" YAML.
func isYAMLPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	}
	return false
}

// migrateLegacy converts the pre-JSON config.yaml to config.json, once. It acts
// only when jsonPath is the default location, config.json does not exist yet,
// and config.yaml does — every other combination is a no-op returning ("", nil).
//
// The YAML file is left where it is: it may be in a dotfiles repo, and deleting
// a user's file to tidy up after ourselves is not a migration's call. After
// this, nothing reads it — the log line says so, so an edit to the old file
// that "does nothing" has an explanation.
//
// On failure it returns the legacy path and the error, so Load can fall back to
// reading the YAML rather than dropping the user's settings.
func migrateLegacy(jsonPath string) (legacy string, err error) {
	if jsonPath == "" || jsonPath != DefaultPath() {
		return "", nil
	}
	err = withLock(jsonPath, func() error {
		legacy, err = migrateLegacyLocked(jsonPath)
		return err
	})
	return legacy, err
}

// migrateLegacyLocked is migrateLegacy's body; the caller holds the lock. Split
// out so WriteSection can migrate inside the lock it already holds (flock is
// per open file, so re-locking from the same process would deadlock).
func migrateLegacyLocked(jsonPath string) (string, error) {
	if jsonPath != DefaultPath() {
		return "", nil
	}
	if _, err := os.Stat(jsonPath); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return "", nil // already migrated (or unreadable, which Load reports)
	}
	legacy := LegacyPath()
	data, err := os.ReadFile(legacy)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil // a fresh install: nothing to migrate
		}
		return legacy, err
	}
	cfg, err := parse(data, true)
	if err != nil {
		return legacy, err
	}
	if err := saveJSONLocked(jsonPath, cfg); err != nil {
		return legacy, err
	}
	log.Printf("config: migrated %s to %s; the YAML file is no longer read", legacy, jsonPath)
	return "", nil
}

// saveJSON writes cfg as indented JSON, preserving top-level keys Config does
// not model (see the file comment).
func saveJSON(path string, cfg Config) error {
	return withLock(path, func() error { return saveJSONLocked(path, cfg) })
}

func saveJSONLocked(path string, cfg Config) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	// Carry foreign sections through. A key Config DOES model is never copied
	// from disk, even when the marshalled struct omitted it (hosts: [] is
	// omitempty): that absence is the new value, and copying the old list back
	// would make removing the last host impossible.
	old, err := readDoc(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Unreadable or malformed: there is nothing trustworthy to preserve, and
		// refusing to save would leave the user unable to fix the file from the
		// settings screen. Say what happened.
		log.Printf("config: %s is unreadable (%v); rewriting it from the running config", path, err)
	}
	known := knownKeys()
	for k, v := range old {
		if !slices.Contains(known, k) {
			doc[k] = v
		}
	}
	return writeDoc(path, doc)
}

// ReadSection decodes one top-level key of the JSON config at path into out.
// found is false (with a nil error) when the file or the key is absent. It is
// for sections Config does not model — today the Mac app's "app" block.
func ReadSection(path, key string, out any) (found bool, err error) {
	doc, err := readDoc(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	raw, ok := doc[key]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return true, fmt.Errorf("config %s: %s: %w", path, key, err)
	}
	return true, nil
}

// WriteSection replaces one top-level key of the JSON config at path, leaving
// every other key as it was. It refuses keys Config models: those belong to
// catway, whose in-memory copy would silently overwrite this write on its next
// save.
//
// When the file does not exist yet at the default location, the legacy YAML is
// migrated first. Otherwise the Mac app's first save would create a config.json
// holding only "app" — and catway, seeing config.json present, would never
// migrate, dropping every setting the user had in config.yaml.
func WriteSection(path, key string, v any) error {
	if slices.Contains(knownKeys(), key) {
		return fmt.Errorf("config: %q is catway's section, not writable by section", key)
	}
	if isYAMLPath(path) {
		return fmt.Errorf("config: %s is YAML; sections are only kept in the JSON config", path)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", key, err)
	}
	return withLock(path, func() error {
		if _, err := migrateLegacyLocked(path); err != nil {
			return fmt.Errorf("migrate legacy config: %w", err)
		}
		doc, err := readDoc(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err // a malformed file is catway's to report; don't paper over it
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		doc[key] = raw
		return writeDoc(path, doc)
	})
}

// readDoc reads the file as a map of top-level keys to raw JSON. An empty file
// is an empty document.
func readDoc(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(data)) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return doc, nil
}

// writeDoc encodes doc with a stable key order — Config's own field order
// first, then any foreign keys alphabetically — and replaces the file
// atomically. json.Marshal on a map would sort every key alphabetically, which
// scatters "server" into the middle of the file and makes each save a noisy
// diff for anyone keeping the file in git.
func writeDoc(path string, doc map[string]json.RawMessage) error {
	order := make([]string, 0, len(doc))
	for _, k := range knownKeys() {
		if _, ok := doc[k]; ok {
			order = append(order, k)
		}
	}
	var foreign []string
	for k := range doc {
		if !slices.Contains(order, k) {
			foreign = append(foreign, k)
		}
	}
	slices.Sort(foreign)
	order = append(order, foreign...)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range order {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k) // a string key cannot fail to marshal
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(doc[k])
	}
	buf.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	out.WriteByte('\n')
	return writeFileAtomic(path, out.Bytes())
}

// writeFileAtomic writes via a temp file in the same directory and a rename, so
// a reader (catway's reload, the other process) never sees a half-written file.
// A new file is created 0600: it can name hosts, peers and a remote catway URL,
// none of which is anyone else's business. An existing file keeps its mode.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("save config %s: %w", path, err)
	}
	mode := fs.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("save config %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("save config %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("save config %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save config %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("save config %s: %w", path, err)
	}
	return nil
}

// knownKeys is Config's top-level JSON keys in field order, read off the struct
// tags so a new section is covered the moment it is added to Config.
func knownKeys() []string {
	t := reflect.TypeOf(Config{})
	out := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}
