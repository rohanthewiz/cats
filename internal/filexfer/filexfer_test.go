package filexfer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write a file and return its path.
func seed(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStat(t *testing.T) {
	dir := t.TempDir()
	p := seed(t, dir, "a.txt", []byte("hello"))

	res := Do(OpRequest{Op: OpStat, Path: p})
	if res.Error != "" {
		t.Fatalf("stat: %s", res.Error)
	}
	if res.Size != 5 || res.Dir || res.Path != p {
		t.Errorf("stat = %+v", res)
	}
	if res.MTime == 0 {
		t.Error("stat carried no mtime")
	}

	if d := Do(OpRequest{Op: OpStat, Path: dir}); !d.Dir {
		t.Errorf("stat of a directory: %+v", d)
	}
	// A missing path is an error rather than a result with Exists false: every
	// caller here is about to read or write that exact path.
	if m := Do(OpRequest{Op: OpStat, Path: filepath.Join(dir, "nope")}); m.Error == "" {
		t.Error("stat of a missing path succeeded")
	}
}

// A relative path resolves against Base, and "~" against the running user's
// home — both on THIS machine, which is the whole point of the field.
func TestPathResolution(t *testing.T) {
	dir := t.TempDir()
	seed(t, dir, "a.txt", []byte("x"))

	res := Do(OpRequest{Op: OpStat, Path: "a.txt", Base: dir})
	if res.Error != "" {
		t.Fatalf("relative stat: %s", res.Error)
	}
	if res.Path != filepath.Join(dir, "a.txt") {
		t.Errorf("resolved to %q, want %q", res.Path, filepath.Join(dir, "a.txt"))
	}
	if e := Do(OpRequest{Op: OpStat, Path: "  "}); e.Error == "" {
		t.Error("a blank path was accepted")
	}
}

func TestReadWholeFile(t *testing.T) {
	dir := t.TempDir()
	p := seed(t, dir, "a.txt", []byte("hello world"))

	res := Do(OpRequest{Op: OpRead, Path: p})
	if res.Error != "" {
		t.Fatalf("read: %s", res.Error)
	}
	if string(res.Data) != "hello world" || !res.EOF || res.Size != 11 {
		t.Errorf("read = %+v", res)
	}
}

// The refusal that is the safety story of this API: asking for a whole file
// bigger than one chunk is an ERROR, not the first chunk of it. A prefix with
// EOF false is indistinguishable from a whole file to a caller that did not
// check, and the caller who does not range is exactly that caller.
func TestReadWholeFileTooLargeIsRefused(t *testing.T) {
	dir := t.TempDir()
	p := seed(t, dir, "big", bytes.Repeat([]byte("x"), MaxChunk+1))

	res := Do(OpRequest{Op: OpRead, Path: p})
	if res.Error == "" {
		t.Fatalf("a whole-file read of %d bytes was answered: len=%d eof=%v",
			MaxChunk+1, len(res.Data), res.EOF)
	}
	if !strings.Contains(res.Error, "offset and length") {
		t.Errorf("refusal does not name the fix: %q", res.Error)
	}
	// Ranged explicitly, the same file reads fine.
	r := Do(OpRequest{Op: OpRead, Path: p, Offset: 0, Length: 16})
	if r.Error != "" || len(r.Data) != 16 || r.EOF {
		t.Errorf("ranged read = %+v (err %q)", len(r.Data), r.Error)
	}
}

func TestReadRanges(t *testing.T) {
	dir := t.TempDir()
	p := seed(t, dir, "a.txt", []byte("0123456789"))

	cases := []struct {
		name        string
		off, length int64
		want        string
		wantEOF     bool
	}{
		{"head", 0, 4, "0123", false},
		{"middle", 4, 3, "456", false},
		{"tail exactly", 7, 3, "789", true},
		{"past the end of the range", 7, 99, "789", true},
		{"offset at eof", 10, 4, "", true},
		{"offset past eof", 99, 4, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := Do(OpRequest{Op: OpRead, Path: p, Offset: c.off, Length: c.length})
			if res.Error != "" {
				t.Fatalf("read: %s", res.Error)
			}
			if string(res.Data) != c.want || res.EOF != c.wantEOF {
				t.Errorf("read = %q eof=%v, want %q eof=%v", res.Data, res.EOF, c.want, c.wantEOF)
			}
			if res.Offset != c.off {
				t.Errorf("result offset %d, want %d", res.Offset, c.off)
			}
		})
	}
}

// Length is clamped rather than refused: the transports do not care what was
// asked for, and a caller that asks for too much wants as much as it can have.
func TestReadLengthClamped(t *testing.T) {
	dir := t.TempDir()
	p := seed(t, dir, "big", bytes.Repeat([]byte("y"), MaxChunk+500))

	res := Do(OpRequest{Op: OpRead, Path: p, Length: MaxChunk * 4})
	if res.Error != "" {
		t.Fatalf("read: %s", res.Error)
	}
	if len(res.Data) != MaxChunk || res.EOF {
		t.Errorf("read %d bytes eof=%v, want %d and not eof", len(res.Data), res.EOF, MaxChunk)
	}
}

func TestReadRefusals(t *testing.T) {
	dir := t.TempDir()
	if r := Do(OpRequest{Op: OpRead, Path: dir}); !strings.Contains(r.Error, "is a directory") {
		t.Errorf("read of a directory: %q", r.Error)
	}
	if r := Do(OpRequest{Op: OpRead, Path: filepath.Join(dir, "gone")}); r.Error == "" {
		t.Error("read of a missing file succeeded")
	}
	if r := Do(OpRequest{Op: OpRead, Path: filepath.Join(dir, "gone"), Offset: -1}); r.Error == "" {
		t.Error("negative offset accepted")
	}
	if r := Do(OpRequest{Op: "sideways", Path: dir}); !strings.Contains(r.Error, "sideways") {
		t.Errorf("unknown op should name itself: %q", r.Error)
	}
}

// A one-shot write — no More — lands the file under its final name, which is
// what makes the naive caller's default the safe one.
func TestWriteOneShotIsAtomic(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.txt")

	res := Do(OpRequest{Op: OpWrite, Path: dst, Data: []byte("done")})
	if res.Error != "" {
		t.Fatalf("write: %s", res.Error)
	}
	if !res.Complete || res.Written != 4 || res.Size != 4 {
		t.Errorf("write = %+v", res)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "done" {
		t.Fatalf("file = %q, %v", got, err)
	}
	if leftovers(t, dir) != 1 {
		t.Error("a part file was left behind by a complete write")
	}
}

// The part-file discipline: bytes are not visible under the destination's name
// until the chunk that is not More, so an interrupted transfer cannot be read as
// a whole file by something else.
func TestWriteChunkedHoldsBackUntilComplete(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")

	if r := Do(OpRequest{Op: OpWrite, Path: dst, Data: []byte("AAAA"), More: true}); r.Error != "" {
		t.Fatalf("chunk 1: %s", r.Error)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("the destination exists while the transfer is still running")
	}
	if _, err := os.Stat(partPath(dst)); err != nil {
		t.Fatalf("no part file during the transfer: %v", err)
	}
	if r := Do(OpRequest{Op: OpWrite, Path: dst, Data: []byte("BBBB"), Offset: 4, More: true}); r.Error != "" {
		t.Fatalf("chunk 2: %s", r.Error)
	}
	last := Do(OpRequest{Op: OpWrite, Path: dst, Data: []byte("CC"), Offset: 8})
	if last.Error != "" {
		t.Fatalf("last chunk: %s", last.Error)
	}
	if !last.Complete {
		t.Error("the last chunk did not report Complete")
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "AAAABBBBCC" {
		t.Fatalf("file = %q, %v", got, err)
	}
	if _, err := os.Stat(partPath(dst)); err == nil {
		t.Error("the part file survived the rename")
	}
}

// An abandoned transfer leaves a fragment under a name nobody will mistake for
// the real file — the whole reason for the part file.
func TestWriteAbandonedLeavesOnlyAFragment(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")

	if r := Do(OpRequest{Op: OpWrite, Path: dst, Data: []byte("AAAA"), More: true}); r.Error != "" {
		t.Fatalf("chunk 1: %s", r.Error)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("the destination name exists after an abandoned transfer")
	}
	frag := partPath(dst)
	if !strings.HasPrefix(filepath.Base(frag), ".") || !strings.HasSuffix(frag, ".cats-part") {
		t.Errorf("fragment name %q is neither hidden nor self-explaining", frag)
	}
}

// Replacing a file is the one irreversible thing here, so it takes an explicit
// flag — checked before the bytes move, and again at the rename.
func TestWriteRefusesToClobber(t *testing.T) {
	dir := t.TempDir()
	dst := seed(t, dir, "keep.txt", []byte("original"))

	res := Do(OpRequest{Op: OpWrite, Path: dst, Data: []byte("new")})
	if res.Error == "" {
		t.Fatal("an existing file was overwritten without the flag")
	}
	if !strings.Contains(res.Error, "overwrite") {
		t.Errorf("refusal does not name the fix: %q", res.Error)
	}
	if got, _ := os.ReadFile(dst); string(got) != "original" {
		t.Errorf("the refused write still changed the file: %q", got)
	}
	if leftovers(t, dir) != 1 {
		t.Error("the refusal left a fragment behind")
	}

	ok := Do(OpRequest{Op: OpWrite, Path: dst, Data: []byte("new"), Overwrite: true})
	if ok.Error != "" {
		t.Fatalf("overwrite: %s", ok.Error)
	}
	if got, _ := os.ReadFile(dst); string(got) != "new" {
		t.Errorf("file = %q", got)
	}
}

// A destination that appeared DURING a transfer is refused on the next chunk —
// the overwrite check runs on every chunk, not only the first — and the now-dead
// fragment is cleaned up rather than left as litter under a name nobody will
// ever look for.
func TestWriteRefusesAClobberThatAppearedMidTransfer(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "race.txt")

	if r := Do(OpRequest{Op: OpWrite, Path: dst, Data: []byte("AAAA"), More: true}); r.Error != "" {
		t.Fatalf("chunk 1: %s", r.Error)
	}
	seed(t, dir, "race.txt", []byte("somebody else")) // arrives mid-transfer

	last := Do(OpRequest{Op: OpWrite, Path: dst, Data: []byte("BB"), Offset: 4})
	if last.Error == "" {
		t.Fatal("the rename clobbered a file that appeared mid-transfer")
	}
	if got, _ := os.ReadFile(dst); string(got) != "somebody else" {
		t.Errorf("file = %q", got)
	}
	if _, err := os.Stat(partPath(dst)); err == nil {
		t.Error("the refusal left its fragment behind")
	}
}

func TestWriteMode(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "script.sh")
	if r := Do(OpRequest{Op: OpWrite, Path: dst, Data: []byte("#!/bin/sh\n"), Mode: 0o755}); r.Error != "" {
		t.Fatalf("write: %s", r.Error)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", fi.Mode().Perm())
	}
}

func TestWriteRefusals(t *testing.T) {
	dir := t.TempDir()
	if r := Do(OpRequest{Op: OpWrite, Path: dir, Data: []byte("x")}); !strings.Contains(r.Error, "is a directory") {
		t.Errorf("write onto a directory: %q", r.Error)
	}
	missing := filepath.Join(dir, "no", "such", "f.txt")
	if r := Do(OpRequest{Op: OpWrite, Path: missing, Data: []byte("x")}); !strings.Contains(r.Error, "no such directory") {
		t.Errorf("write into a missing directory: %q", r.Error)
	}
	over := Do(OpRequest{Op: OpWrite, Path: filepath.Join(dir, "f"), Data: make([]byte, MaxChunk+1)})
	if over.Error == "" {
		t.Error("an oversized chunk was accepted")
	}
	if r := Do(OpRequest{Op: OpWrite, Path: filepath.Join(dir, "f"), Offset: -1}); r.Error == "" {
		t.Error("negative offset accepted")
	}
}

// A retried opening chunk truncates, so a transfer restarted after a failure
// cannot inherit the tail of the abandoned one.
func TestWriteRetryTruncates(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")

	if r := Do(OpRequest{Op: OpWrite, Path: dst, Data: bytes.Repeat([]byte("A"), 100), More: true}); r.Error != "" {
		t.Fatalf("first attempt: %s", r.Error)
	}
	if r := Do(OpRequest{Op: OpWrite, Path: dst, Data: []byte("BB")}); r.Error != "" {
		t.Fatalf("restart: %s", r.Error)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "BB" {
		t.Errorf("file = %q (%d bytes); the restart inherited the abandoned tail", got, len(got))
	}
}

// A read and a write of the same bytes round-trip, which is the property
// `catctl cp` is built on.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	payload := bytes.Repeat([]byte("cats\x00\xff"), 40000) // binary, > 1 chunk
	src := seed(t, dir, "src.bin", payload)
	dst := filepath.Join(dir, "dst.bin")

	var off int64
	for {
		r := Do(OpRequest{Op: OpRead, Path: src, Offset: off, Length: 1 << 16})
		if r.Error != "" {
			t.Fatalf("read at %d: %s", off, r.Error)
		}
		w := Do(OpRequest{Op: OpWrite, Path: dst, Data: r.Data, Offset: off, More: !r.EOF})
		if w.Error != "" {
			t.Fatalf("write at %d: %s", off, w.Error)
		}
		off += int64(len(r.Data))
		if r.EOF {
			break
		}
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round trip differs: %d bytes vs %d", len(got), len(payload))
	}
}

// leftovers counts the entries in dir, so a test can assert that nothing but
// the file it asked for is there.
func leftovers(t *testing.T, dir string) int {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(ents)
}
