// Package filexfer is the file-transfer primitive: read a slice of a file,
// write a slice of a file, stat a path — on whichever machine is running this
// code.
//
// It exists once and is called from two processes. cathost calls it to serve a
// request that arrived over the orchestration seam (a file on ITS disk), and
// catway calls it directly for its own host. That is the same arrangement
// internal/worktree has, and for the same reason: a second implementation of
// "write these bytes at this offset" would be a second set of edge cases about
// modes, partial writes and overwrite refusals, drifting apart the first time
// only one of them was fixed.
//
// # Why ranges, and not a stream
//
// The obvious design is a streaming transfer — open, pump chunks, close — and
// it is the wrong one HERE because of what sits between the two ends:
//
//	catctl ──ctlproto──▶ catway ──seam frame──▶ cathost ──▶ disk
//	   (or a pane's catctl ──control relay──▶ catway, on a remote box)
//
// Every hop is a whole-message transport with its own ceiling.
// orchestration.MaxFrameSize is 8 MiB per seam frame; the control relay caps
// one client line at 4 MiB (controlRelayMaxLine); and JSON renders []byte as
// base64, so a payload costs 4/3 of its size on all of them. A streaming API
// would have to invent its own chunking to fit inside those ceilings anyway,
// and would then own the resulting state — half-open transfers, abandoned
// readers, a cathost holding file descriptors for a catway that went away.
//
// So the chunking is the CALLER's loop, over a stateless positional primitive.
// A get with an offset and a length is one request and one answer; nothing is
// held open between them; a dropped link loses a chunk rather than a transfer;
// and the same call serves the one-shot case ("read this 40-line config") with
// no loop at all.
//
// # What is NOT here
//
// No recursion, no directories, no globbing, no preserved ownership. `cp -r` is
// a client-side walk over stat and get, and building the walk in here would put
// it on the far side of a network hop from the thing that knows what it wants.
package filexfer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rohanthewiz/cats/internal/pathpick"
)

// MaxChunk bounds one request's payload in either direction, and is a constant
// rather than a configuration key because it is not a preference — it is what
// the transports above allow.
//
// 1 MiB of file becomes ~1.37 MiB of base64, which fits inside the control
// relay's 4 MiB line and the seam's 8 MiB frame with the rest of the message
// and a wide margin to spare. A knob here would let an operator choose a value
// that makes every transfer through a relayed pane fail, with a symptom
// ("connection closed") a long way from the setting that caused it.
const MaxChunk = 1 << 20

// DefaultMode is the permission a newly created file gets when the caller names
// none. It matches what a shell redirect would produce, umask aside.
const DefaultMode = 0o644

// Op names the three things this package does. It is a string rather than an
// iota so an unknown op from a newer client reads as itself in the error.
type Op string

const (
	OpStat  Op = "stat"
	OpRead  Op = "read"
	OpWrite Op = "write"
)

// OpRequest is one file operation, shaped to cross the seam as-is (see
// orchestration.RequestFile) so that both ends call Do with the same value.
//
// Path is resolved on the machine that runs the operation: "~" is that user's
// home and a relative path resolves against Base, which callers set to the
// addressed pane's live cwd. Neither can be resolved by the asking side, for
// the reason every path in this codebase travels raw since the multi-host
// slice — the same string on two machines names two files.
type OpRequest struct {
	Op   Op     `json:"op"`
	Path string `json:"path"`
	Base string `json:"base,omitempty"`

	// Offset is where a read starts, and where a write lands. Length bounds a
	// read; zero means "as much as is allowed", with one exception that is the
	// whole safety story of this API — see Do.
	Offset int64 `json:"offset,omitempty"`
	Length int64 `json:"length,omitempty"`

	// Data is the payload of a write.
	Data []byte `json:"data,omitempty"`
	// More marks a write that is one chunk of several, and its DEFAULT is the
	// load-bearing part: false means "this put is the whole file", so the naive
	// one-shot caller — the common one — gets an atomic write with no flag at
	// all. A chunking loop sets it on every chunk but the last.
	More bool `json:"more,omitempty"`
	// Mode is the permission for a file this write creates. Zero means
	// DefaultMode. It is not applied to a file that already exists: a transfer
	// should not silently re-permission somebody's file.
	Mode uint32 `json:"mode,omitempty"`
	// Overwrite allows a write to replace an existing file. Default false, so a
	// transfer that would clobber something is refused rather than performed —
	// the one irreversible thing this package can do.
	Overwrite bool `json:"overwrite,omitempty"`
}

// OpResult answers an OpRequest. Error carries a FAILED operation's reason and
// is checked first: a result with an Error has nothing else populated.
//
// A failure is carried in the result rather than returned as a Go error because
// this value crosses the seam, where "the file does not exist" is an answer to
// the question that was asked — not a broken connection.
type OpResult struct {
	// Path is the resolved absolute path, which is worth reporting for every op:
	// the caller sent "~/notes.md" and only the answering machine knows what
	// that is.
	Path  string `json:"path"`
	Error string `json:"error,omitempty"`

	Size  int64  `json:"size,omitempty"`
	Mode  uint32 `json:"mode,omitempty"`
	Dir   bool   `json:"dir,omitempty"`
	MTime int64  `json:"mtime,omitempty"` // unix seconds

	// Offset echoes where a read actually started, and Data is what it read.
	// EOF reports that the read reached the end of the file, which is how a
	// chunking loop knows to stop without a second stat racing the first.
	Offset int64  `json:"offset,omitempty"`
	Data   []byte `json:"data,omitempty"`
	EOF    bool   `json:"eof,omitempty"`

	// Written is how many bytes a write put down, and Complete reports that the
	// file is now in place under its final name (see the part-file discipline in
	// write).
	Written  int  `json:"written,omitempty"`
	Complete bool `json:"complete,omitempty"`
}

// errResult is a failed answer for a path that may or may not have resolved.
func errResult(path, msg string) OpResult { return OpResult{Path: path, Error: msg} }

// Do runs one operation and never returns an error: every failure is an
// OpResult.Error. Safe to call off any goroutine; it holds no state between
// calls, which is what makes a resumed or retried chunk simply correct.
func Do(req OpRequest) OpResult {
	path := pathpick.Expand(req.Path, req.Base)
	if strings.TrimSpace(req.Path) == "" {
		return errResult("", "no path given")
	}
	switch req.Op {
	case OpStat:
		return stat(path)
	case OpRead:
		return read(path, req)
	case OpWrite:
		return write(path, req)
	default:
		return errResult(path, "unknown file op "+string(req.Op))
	}
}

// stat answers what a path IS. A missing path is an error rather than a result
// with Exists false, because unlike a half-typed directory in the path picker
// there is no interactive caller here refining a guess: every caller of stat is
// about to read or write that exact path.
func stat(path string) OpResult {
	fi, err := os.Stat(path)
	if err != nil {
		return errResult(path, statErr(err))
	}
	return OpResult{
		Path:  path,
		Size:  fi.Size(),
		Mode:  uint32(fi.Mode().Perm()),
		Dir:   fi.IsDir(),
		MTime: fi.ModTime().Unix(),
	}
}

// read returns one slice of a file.
//
// The length rule is the one piece of policy in this package, and it exists to
// kill a silent-truncation bug rather than to enforce a quota:
//
//   - Length > 0 — the caller is ranging deliberately. Clamped to MaxChunk,
//     because the transports do not care what was asked for.
//   - Length == 0, Offset > 0 — a chunking loop that stopped naming a size.
//     Reads up to MaxChunk.
//   - Length == 0, Offset == 0 — "give me this file". Answered in full when it
//     fits, and REFUSED when it does not, naming the size.
//
// That last refusal is the point. The alternative — hand back the first MaxChunk
// bytes with EOF false — turns "read my config" into a prefix that looks exactly
// like a whole file to any caller that did not think to check a flag, and the
// callers that did not think to check are precisely the ones asking for a small
// file. A caller that means to range says so.
func read(path string, req OpRequest) OpResult {
	fi, err := os.Stat(path)
	if err != nil {
		return errResult(path, statErr(err))
	}
	if fi.IsDir() {
		return errResult(path, "is a directory")
	}
	size := fi.Size()
	if req.Offset < 0 {
		return errResult(path, "negative offset")
	}

	length := req.Length
	switch {
	case length > MaxChunk:
		length = MaxChunk
	case length <= 0 && req.Offset > 0:
		length = MaxChunk
	case length <= 0:
		if size > MaxChunk {
			return errResult(path, fmt.Sprintf(
				"file is %d bytes, larger than the %d-byte transfer chunk: read it with an explicit offset and length",
				size, MaxChunk))
		}
		length = size
	}
	// A read starting past the end is not an error — it is the empty tail, and
	// it is what a chunking loop asks for when the file shrank under it. EOF
	// says so.
	if req.Offset >= size {
		return OpResult{Path: path, Size: size, Mode: uint32(fi.Mode().Perm()), Offset: req.Offset, EOF: true}
	}
	if remaining := size - req.Offset; length > remaining {
		length = remaining
	}

	f, err := os.Open(path)
	if err != nil {
		return errResult(path, statErr(err))
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, length)
	// ReadAt rather than Seek+Read: it is the call that means "this offset" with
	// no shared cursor, and it fills the buffer completely or says why.
	n, err := f.ReadAt(buf, req.Offset)
	if err != nil && err != io.EOF {
		return errResult(path, err.Error())
	}
	return OpResult{
		Path:   path,
		Size:   size,
		Mode:   uint32(fi.Mode().Perm()),
		Offset: req.Offset,
		Data:   buf[:n],
		EOF:    req.Offset+int64(n) >= size,
	}
}

// write puts one slice of bytes down, through a part file.
//
// The destination is never opened directly. A write at offset 0 creates
// "<dir>/.<name>.cats-part", later offsets land in that same part file, and the
// write that is not More renames it into place. So an interrupted transfer
// leaves a visible fragment with a name nobody will mistake for the real thing,
// instead of a truncated file under the name a script is about to read — which
// is the failure the whole discipline is here to prevent, since the client
// doing the chunking is on the far side of a network that can drop.
//
// The overwrite refusal is checked twice: at the start, so a doomed transfer
// fails before its bytes cross the wire, and again at the rename, because the
// first check was minutes ago on a machine somebody else is also using.
func write(path string, req OpRequest) OpResult {
	if len(req.Data) > MaxChunk {
		return errResult(path, fmt.Sprintf("chunk is %d bytes, over the %d-byte limit", len(req.Data), MaxChunk))
	}
	if req.Offset < 0 {
		return errResult(path, "negative offset")
	}
	if fi, err := os.Stat(path); err == nil {
		if fi.IsDir() {
			return errResult(path, "is a directory")
		}
		if !req.Overwrite {
			// A transfer already in flight when the destination appeared has a
			// fragment on disk, and it is dead the moment this refusal is
			// returned — so it goes now rather than being left as litter under a
			// name nobody will ever look for. On the opening chunk of a doomed
			// transfer there is nothing there and this is a no-op.
			_ = os.Remove(partPath(path))
			return errResult(path, "file exists (pass overwrite to replace it)")
		}
	}
	dir := filepath.Dir(path)
	if !pathpick.IsDir(dir) {
		return errResult(path, "no such directory: "+dir)
	}
	part := partPath(path)

	mode := os.FileMode(req.Mode).Perm()
	if mode == 0 {
		mode = DefaultMode
	}

	flags := os.O_WRONLY | os.O_CREATE
	if req.Offset == 0 {
		// Truncate on the opening chunk so a retried transfer starts clean
		// rather than inheriting the tail of a longer abandoned one.
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, mode)
	if err != nil {
		return errResult(path, statErr(err))
	}
	n, werr := f.WriteAt(req.Data, req.Offset)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return errResult(path, werr.Error())
	}

	if req.More {
		return OpResult{Path: path, Written: n}
	}
	if _, err := os.Stat(path); err == nil && !req.Overwrite {
		_ = os.Remove(part) // do not leave a fragment behind a refusal
		return errResult(path, "file exists (pass overwrite to replace it)")
	}
	if err := os.Rename(part, path); err != nil {
		return errResult(path, err.Error())
	}
	// chmod after the rename rather than relying on the create mode: the part
	// file may have been created by an earlier chunk under an earlier mode, and
	// umask has had its say in between.
	if req.Mode != 0 {
		_ = os.Chmod(path, mode)
	}
	size := int64(0)
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}
	return OpResult{Path: path, Written: n, Complete: true, Size: size}
}

// partPath is where a transfer's bytes live until it finishes. Dot-prefixed so
// it stays out of an ordinary listing, and suffixed so a fragment that outlives
// its transfer explains itself to whoever finds it.
func partPath(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".cats-part")
}

// statErr trims the "stat /path:" prefix Go's fs errors carry. The caller is
// shown the resolved path in OpResult.Path already, and a message that repeats
// it reads as a bug ("/etc/hosts: stat /etc/hosts: no such file").
func statErr(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
