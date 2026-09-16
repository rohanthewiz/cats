package peersync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// cats-todo's on-disk layout, mirrored here rather than imported: cats-todo is
// a `package main` and its store lives there. What is copied is only the
// contract that is already public in its README — where the files are and
// that each is a JSON array of rows — never the row schema.
const (
	// todoDirName is the per-project backlog directory beside the code.
	todoDirName = ".cats-todo"
	// todoFileName is the backlog file inside it (and inside the global dir).
	todoFileName = "todos.json"
	// todoConfigDirEnv overrides where the global backlog lives.
	todoConfigDirEnv = "CATS_TODO_CONFIG_DIR"
	// todoImagesDir is the attachment root beside todos.json; rows reference
	// files as "images/<id>/<name>" relative to the backlog directory.
	todoImagesDir = "images"
)

// Row field names this package reads. Everything else in a row is opaque.
const (
	fieldID       = "id"
	fieldTitle    = "title"
	fieldPrompt   = "prompt"
	fieldDone     = "done"
	fieldFrozen   = "frozen"
	fieldImages   = "images"
	fieldSchedule = "schedule"
)

// Attachment limits. A screenshot is a few hundred KB; anything past the
// per-file cap is left behind with a note rather than making one prompt's
// attachment the reason a whole sync's request body is refused.
const (
	maxAttachmentBytes = 4 << 20
	maxBacklogFiles    = 32 << 20 // total attachment bytes carried per backlog
)

// GlobalTodoDir is where the global backlog lives: $CATS_TODO_CONFIG_DIR,
// then $XDG_CONFIG_HOME/cats-todo, then ~/.config/cats-todo — the chain
// cats-todo itself walks.
func GlobalTodoDir() (string, error) {
	if d := os.Getenv(todoConfigDirEnv); d != "" {
		return d, nil
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "cats-todo"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "cats-todo"), nil
}

// ProjectTodoDir is a project's backlog directory.
func ProjectTodoDir(project string) string { return filepath.Join(project, todoDirName) }

// readTodos loads a backlog file as generic rows. A missing file is an empty
// backlog (the first-run state), not an error; an unparsable one is, because
// merging into a file we cannot read is how a backlog gets clobbered.
func readTodos(file string) ([]map[string]any, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return rows, nil
}

// writeTodos writes rows back the way cats-todo does: two-space indent, a
// trailing newline, and a temp-file-plus-rename so a crash mid-write leaves
// the old file rather than half of the new one.
func writeTodos(file string, rows []map[string]any) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if rows == nil {
		rows = []map[string]any{} // "[]", never "null" — cats-todo reads either, but the file should look like its own
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".todos-*.tmp")
	if err != nil {
		return err
	}
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Chmod(0o644)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), file)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// collectBacklog reads one backlog directory into a Backlog, attachments
// included. ok is false when the directory holds no backlog at all, which
// for a project means "this project never ran cats-todo init" and is not
// worth a row in anyone's report.
func collectBacklog(dir string) (b Backlog, ok bool, err error) {
	file := filepath.Join(dir, todoFileName)
	if _, err := os.Stat(file); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Backlog{}, false, nil
		}
		return Backlog{}, false, err
	}
	rows, err := readTodos(file)
	if err != nil {
		return Backlog{}, false, err
	}
	b = Backlog{Todos: rows}
	var total int
	for _, row := range rows {
		for _, rel := range rowImages(row) {
			name, ok := safeAttachmentPath(rel)
			if !ok {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
			if err != nil || len(data) > maxAttachmentBytes || total+len(data) > maxBacklogFiles {
				continue // the row still travels; the receiver notes the missing file
			}
			if b.Files == nil {
				b.Files = map[string][]byte{}
			}
			b.Files[name] = data
			total += len(data)
		}
	}
	return b, true, nil
}

// rowImages reads a row's attachment list, tolerating its absence.
func rowImages(row map[string]any) []string {
	raw, _ := row[fieldImages].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// safeAttachmentPath accepts only the shape cats-todo writes — a clean
// forward-slashed path under images/ with no traversal — so a row from
// another machine can name a file to write but never where outside the
// backlog directory to write it.
func safeAttachmentPath(rel string) (string, bool) {
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return "", false
	}
	clean := path.Clean(rel)
	if clean != rel || !strings.HasPrefix(clean, todoImagesDir+"/") || strings.Contains(clean, "..") {
		return "", false
	}
	return clean, true
}

// --- merge -------------------------------------------------------------------

// MergeResult is the arithmetic of one backlog merge.
type MergeResult struct {
	Added     int // rows this side did not have
	Completed int // rows this side had open that the other side had finished
	Unchanged int // rows identical on both sides
	Dupes     int // rows with the same text under a different id — not added
	Conflicts int // rows with the same id that differ, kept as they were here
	NoFiles   int // added rows whose attachments could not be brought across
	Notes     []string
}

// changed reports whether the merge wrote anything.
func (m MergeResult) changed() bool { return m.Added > 0 || m.Completed > 0 }

// mergeTodos folds incoming rows into local rows and returns the new list.
//
// Identity is the row id first — the machine-to-machine case where both ends
// hold the same record — and the text (title + prompt) second, so a prompt that
// was exported by hand earlier and given a new id on arrival is recognized
// rather than doubled. Then, per incoming row:
//
//	no match            → appended (schedule stripped: it names a pane on the
//	                      other machine; attachments copied in via files)
//	same id, identical  → unchanged
//	same id, differs    → the more finished state wins, and ONLY that: a row
//	                      done or frozen over there and open here takes the
//	                      other side's record, because "I finished this" is
//	                      the one edit that is safe to propagate blind. Any
//	                      other difference — text, priority, flags — is a
//	                      conflict: kept as it was here and reported, since
//	                      the rows carry no modification time to break the tie.
//	same text, other id → a duplicate; not added
//
// Rows are never removed, and local rows never reordered: the appended rows
// go at the end, which is where cats-todo puts a new prompt too.
func mergeTodos(local, incoming []map[string]any, files map[string][]byte, dir string) ([]map[string]any, MergeResult) {
	var res MergeResult
	byID := make(map[string]int, len(local))
	byKey := make(map[string]bool, len(local))
	for i, row := range local {
		if id := str(row[fieldID]); id != "" {
			byID[id] = i
		}
		byKey[todoKey(row)] = true
	}
	out := append([]map[string]any(nil), local...)
	for _, in := range incoming {
		id := str(in[fieldID])
		if i, ok := byID[id]; ok && id != "" {
			mine := out[i]
			if sameRow(mine, in) {
				res.Unchanged++
				continue
			}
			if rank(in) > rank(mine) {
				// Take the finished record, but keep what only makes sense
				// here: our attachment paths (theirs may not exist on this
				// disk) and no schedule (theirs named their pane; ours, if
				// any, was for work that is now done).
				merged := clone(in)
				delete(merged, fieldSchedule)
				if imgs, ok := mine[fieldImages]; ok {
					merged[fieldImages] = imgs
				} else {
					delete(merged, fieldImages)
				}
				out[i] = merged
				res.Completed++
				continue
			}
			res.Conflicts++
			res.Notes = append(res.Notes, "conflict: "+todoLabel(mine)+" (kept this side's copy)")
			continue
		}
		if byKey[todoKey(in)] {
			res.Dupes++
			continue
		}
		row := clone(in)
		delete(row, fieldSchedule)
		if rels := rowImages(row); len(rels) > 0 {
			kept, missing := importAttachments(dir, rels, files)
			if missing > 0 {
				res.NoFiles++
			}
			if len(kept) == 0 {
				delete(row, fieldImages)
			} else {
				row[fieldImages] = toAny(kept)
			}
		}
		out = append(out, row)
		byKey[todoKey(row)] = true
		if id != "" {
			byID[id] = len(out) - 1
		}
		res.Added++
	}
	return out, res
}

// importAttachments writes the files an incoming row references under dir
// and returns the references that landed, plus how many did not. A file the
// bundle never carried (too large, unreadable over there) or that cannot be
// written here is dropped from the row rather than left dangling; the text
// is the part with the value, and cats-todo draws a missing image as an error
// on every render.
func importAttachments(dir string, rels []string, files map[string][]byte) (kept []string, missing int) {
	for _, rel := range rels {
		name, ok := safeAttachmentPath(rel)
		if !ok {
			missing++
			continue
		}
		data, ok := files[name]
		if !ok {
			missing++
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if _, err := os.Stat(dst); err == nil {
			kept = append(kept, name) // already here (a re-sync); leave it
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			missing++
			continue
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			missing++
			continue
		}
		kept = append(kept, name)
	}
	return kept, missing
}

// rank orders a row's state: open < frozen < done. "More finished" wins a
// same-id merge; nothing else does.
func rank(row map[string]any) int {
	if b, _ := row[fieldDone].(bool); b {
		return 2
	}
	if b, _ := row[fieldFrozen].(bool); b {
		return 1
	}
	return 0
}

// todoKey is the text identity cats-todo's own import dedupes on.
func todoKey(row map[string]any) string {
	return strings.TrimSpace(str(row[fieldTitle])) + "\x00" + strings.TrimSpace(str(row[fieldPrompt]))
}

// todoLabel names a row for a report line: its title, else the first line of
// its prompt, else its id.
func todoLabel(row map[string]any) string {
	if t := strings.TrimSpace(str(row[fieldTitle])); t != "" {
		return t
	}
	if p := strings.TrimSpace(str(row[fieldPrompt])); p != "" {
		if i := strings.IndexByte(p, '\n'); i >= 0 {
			p = p[:i]
		}
		if len(p) > 60 {
			p = p[:60] + "…"
		}
		return p
	}
	return str(row[fieldID])
}

// sameRow compares two rows ignoring the fields that legitimately differ
// between machines: the schedule (pane ids are local) and the attachment
// paths (the files live on each disk). Comparison is over canonical JSON so
// map ordering and number encoding cannot produce a false difference.
func sameRow(a, b map[string]any) bool {
	return canon(a) == canon(b)
}

func canon(row map[string]any) string {
	c := clone(row)
	delete(c, fieldSchedule)
	delete(c, fieldImages)
	data, _ := json.Marshal(c) // map keys are sorted by encoding/json
	return string(data)
}

func clone(row map[string]any) map[string]any {
	c := make(map[string]any, len(row))
	maps.Copy(c, row)
	return c
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
