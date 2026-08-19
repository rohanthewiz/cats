package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/filexfer"
)

// `catctl cp` — copy a file between this machine and a cathost, or between two
// cathosts.
//
//	catctl cp devbox:/var/log/build.log .
//	catctl cp ./patch.diff devbox:~/work/
//	catctl cp devbox:notes.md laptop:notes.md
//
// It owns its own runner rather than being an entry in the ergonomic verb table,
// because every other verb there is ONE request and this is a loop: stat the
// source, then get and put a chunk at a time until the source says eof. That
// loop is the transfer — see internal/filexfer for why the chunking lives in the
// client and not in the protocol.
//
// The loop is also what makes progress and failure honest. A chunk that fails
// stops the copy, names the byte offset it stopped at, and says the destination
// is left as a `.name.cats-part` fragment — which it is, because file.put only
// renames into place on the chunk that is not `more`.

// cpUsage is the synopsis, shown for a malformed invocation and by `catctl help`.
const cpUsage = "cp [-f] <src> <dst>   (either side may be host:path)"

// runCP parses the operands and drives the transfer. It dispatches before the
// global flag re-parse in run(), like probe and integration, because -f is its
// own flag and the paths must not be read as flags at all.
func runCP(args []string, socket string, timeout time.Duration) int {
	force := false
	var operands []string
	for _, a := range args {
		switch a {
		case "-f", "--force":
			force = true
		case "-h", "--help":
			fmt.Println("usage: catctl " + cpUsage)
			return 0
		default:
			operands = append(operands, a)
		}
	}
	if len(operands) != 2 {
		fmt.Fprintln(os.Stderr, "usage: catctl "+cpUsage)
		return 2
	}
	src, dst := parseCPPath(operands[0]), parseCPPath(operands[1])
	if src.local && dst.local {
		// Refused rather than performed. catctl is not a copy tool, and a `cp`
		// here that quietly did a local copy would be a different program from
		// the one the next argument makes it.
		fmt.Fprintln(os.Stderr, "catctl: cp needs at least one host:path side (use cp(1) for two local paths)")
		return 2
	}

	c := &cpConn{socket: socket, timeout: timeout}
	if err := c.copy(src, dst, force); err != nil {
		fmt.Fprintf(os.Stderr, "catctl: %v\n", err)
		return 1
	}
	return 0
}

// cpPath is one operand: a path, and the host it is on (empty for this machine).
type cpPath struct {
	host  string
	path  string
	local bool
}

// parseCPPath splits "host:path" the way scp does, and for the same reason: it
// is the notation anybody reaching for this command already has in their
// fingers.
//
// A leading "/", "." or "~" makes the operand local no matter what follows, so
// "./weird:name" and "/var/log/a:b" are paths rather than hosts. Anything else
// is remote only when the text before the first colon looks like a host id —
// letters, digits, dot, dash, underscore — which is what cats requires of one.
func parseCPPath(s string) cpPath {
	if s == "" || strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~") {
		return cpPath{path: s, local: true}
	}
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return cpPath{path: s, local: true}
	}
	host := s[:i]
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
		default:
			return cpPath{path: s, local: true}
		}
	}
	return cpPath{host: host, path: s[i+1:]}
}

func (p cpPath) String() string {
	if p.local {
		return p.path
	}
	return p.host + ":" + p.path
}

// cpConn carries what every call in the loop needs. Each call is its own
// connection (ctlproto.Call dials per request), which is a real cost per chunk
// and a deliberate one: the alternative is a held-open socket that has to be
// re-established anyway the moment anything drops, for a transfer whose chunks
// are already a megabyte apiece.
type cpConn struct {
	socket  string
	timeout time.Duration
	seq     int
}

// call issues one §7 command and decodes its result.
func (c *cpConn) call(method string, params any, out any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	c.seq++
	resp, err := ctlproto.Call(c.socket,
		ctlproto.Request{ID: fmt.Sprint(c.seq), Method: method, Params: b}, c.timeout)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Data, out)
}

// copy runs one transfer. The destination is resolved first, because "cp x ." and
// "cp x dir/" have to become a filename before a single byte moves — a transfer
// that discovered its destination was a directory on the final chunk would have
// written a megabyte into a part file for nothing.
func (c *cpConn) copy(src, dst cpPath, force bool) error {
	mode, err := c.srcMode(src)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	dst, err = c.resolveDst(src, dst)
	if err != nil {
		return fmt.Errorf("%s: %w", dst, err)
	}

	var offset int64
	for {
		chunk, eof, err := c.read(src, offset)
		if err != nil {
			return fmt.Errorf("%s at byte %d: %w", src, offset, err)
		}
		// An empty source is still a file: the write has to happen, so the loop
		// body runs at least once and only then tests eof.
		if err := c.write(dst, chunk, offset, !eof, mode, force); err != nil {
			if offset > 0 {
				return fmt.Errorf("%s at byte %d: %w (a .%s.cats-part fragment is left behind)",
					dst, offset, err, filepath.Base(dst.path))
			}
			return fmt.Errorf("%s: %w", dst, err)
		}
		offset += int64(len(chunk))
		if eof {
			break
		}
	}
	fmt.Printf("%s -> %s (%d bytes)\n", src, dst, offset)
	return nil
}

// srcMode stats the source: it refuses a directory before anything moves, and
// returns the permission bits, which the destination is created with so a copied
// script is still executable. That is the one piece of metadata worth carrying
// at this level — owner and timestamps belong to a tool that also does trees.
//
// The size deliberately does NOT come from here. The loop asks the source for
// eof on every chunk instead, so a file that changes size mid-copy ends where it
// ends rather than against a number taken before the first byte moved.
func (c *cpConn) srcMode(src cpPath) (mode uint32, err error) {
	if src.local {
		fi, err := os.Stat(src.path)
		if err != nil {
			return 0, err
		}
		if fi.IsDir() {
			return 0, fmt.Errorf("is a directory (cp copies one file)")
		}
		return uint32(fi.Mode().Perm()), nil
	}
	var res app.FileStatResult
	if err := c.call(app.CmdFileStat,
		app.FileStatParams{Path: src.path, Host: src.host}, &res); err != nil {
		return 0, err
	}
	if res.Dir {
		return 0, fmt.Errorf("is a directory (cp copies one file)")
	}
	return res.Mode, nil
}

// resolveDst turns a destination that names a directory into one that names a
// file, by appending the source's basename — the rule cp(1) and scp both use,
// and the reason "catctl cp devbox:build.log ." does what it looks like.
//
// A trailing slash means directory whether or not one exists there, so a typo'd
// "…/logs/" fails as a missing directory rather than silently creating a file
// called "logs".
func (c *cpConn) resolveDst(src, dst cpPath) (cpPath, error) {
	base := filepath.Base(src.path)
	if strings.HasSuffix(dst.path, "/") {
		dst.path += base
		return dst, nil
	}
	isDir, err := c.isDir(dst)
	if err != nil {
		return dst, err
	}
	if isDir {
		dst.path = strings.TrimSuffix(dst.path, "/") + "/" + base
	}
	return dst, nil
}

// isDir reports whether the destination path exists and is a directory. A path
// that does not exist is not an error here — it is the ordinary case of naming
// the file about to be created.
func (c *cpConn) isDir(p cpPath) (bool, error) {
	if p.local {
		fi, err := os.Stat(p.path)
		if err != nil {
			return false, nil
		}
		return fi.IsDir(), nil
	}
	var res app.FileStatResult
	err := c.call(app.CmdFileStat, app.FileStatParams{Path: p.path, Host: p.host}, &res)
	if err != nil {
		return false, nil
	}
	return res.Dir, nil
}

// read takes one chunk from the source. eof is the SOURCE's answer at the moment
// of the read rather than arithmetic over the size stat'ed at the start: a log
// that gained a line between the two should end the copy where it ended, not
// fail a length check — which is also why the local branch re-stats through the
// open descriptor rather than trusting the earlier number.
func (c *cpConn) read(src cpPath, offset int64) (data []byte, eof bool, err error) {
	length := int64(filexfer.MaxChunk)
	if src.local {
		f, err := os.Open(src.path)
		if err != nil {
			return nil, false, err
		}
		defer func() { _ = f.Close() }()
		fi, err := f.Stat()
		if err != nil {
			return nil, false, err
		}
		size := fi.Size()
		remaining := size - offset
		if remaining <= 0 {
			return nil, true, nil
		}
		if remaining < length {
			length = remaining
		}
		buf := make([]byte, length)
		n, err := f.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			return nil, false, err
		}
		return buf[:n], offset+int64(n) >= size, nil
	}
	var res app.FileGetResult
	if err := c.call(app.CmdFileGet, app.FileGetParams{
		Path: src.path, Host: src.host, Offset: offset, Length: length,
	}, &res); err != nil {
		return nil, false, err
	}
	return res.Data, res.EOF, nil
}

// write puts one chunk down at the destination. more is set for every chunk but
// the last, which is what holds the bytes in a part file until the transfer
// finishes — locally too, so an interrupted copy has the same shape whichever
// machine it was going to.
func (c *cpConn) write(dst cpPath, data []byte, offset int64, more bool, mode uint32, force bool) error {
	if dst.local {
		return localWrite(dst.path, data, offset, more, mode, force)
	}
	return c.call(app.CmdFilePut, app.FilePutParams{
		Path: dst.path, Host: dst.host, Data: data, Offset: offset,
		More: more, Mode: mode, Overwrite: force,
	}, nil)
}

// localWrite is the local half of the destination, and it is filexfer.Do — the
// same function the cathost runs on the far side. A second implementation of
// "write at this offset, through a part file, refusing to clobber" is exactly
// what the shared package exists to prevent.
func localWrite(path string, data []byte, offset int64, more bool, mode uint32, force bool) error {
	res := filexfer.Do(filexfer.OpRequest{
		Op: filexfer.OpWrite, Path: path, Data: data, Offset: offset,
		More: more, Mode: mode, Overwrite: force,
	})
	if res.Error != "" {
		return fmt.Errorf("%s", res.Error)
	}
	return nil
}
