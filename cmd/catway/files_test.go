//go:build ghostty

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/filexfer"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// The slice's rule, applied to bytes: a file under a pane on another machine is
// read BY that machine. Nothing else is possible — the disk is over there — so
// the interesting assertions are about what travels and what does not.
func TestFileGetRunsOnThePanesHost(t *testing.T) {
	o, _, remotePane, _, pdRemote := twoHostOrch(t)
	go o.run()
	d := o.hosts[testRemoteHost]
	d.setFeatures([]string{orchestration.FeatureFileTransfer})

	r := &hostGuardResponder{}
	syncPost(o, func() {
		o.StartFileGet(r, app.FileGetParams{Pane: &remotePane, Path: "~/notes.md", Offset: 8, Length: 64})
	})
	if r.ok || r.fail {
		t.Fatalf("a remote file.get must not resolve before the daemon answers: %+v", r)
	}

	payload := pdRemote.expect(t, orchestration.MsgRequestFile)
	var req orchestration.RequestFile
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req.Req.Op != filexfer.OpRead {
		t.Errorf("op = %q, want %q", req.Req.Op, filexfer.OpRead)
	}
	// Unexpanded on purpose: "~" is the REMOTE user's home, and resolving it
	// here would name a directory in this machine's account.
	if req.Req.Path != "~/notes.md" {
		t.Errorf("path = %q, want the untouched %q", req.Req.Path, "~/notes.md")
	}
	if req.Req.Offset != 8 || req.Req.Length != 64 {
		t.Errorf("range = %d+%d, want 8+64", req.Req.Offset, req.Req.Length)
	}

	d.dispatch(orchestration.MsgFileResult, mustJSON(t, orchestration.NewFileResult(req.ID, filexfer.OpResult{
		Path: "/home/remote/notes.md", Size: 200, Offset: 8, Data: []byte("hello"), EOF: false,
	})))
	syncPost(o, func() {})

	res, isRes := r.data.(app.FileGetResult)
	if !r.ok || !isRes {
		t.Fatalf("no result after the daemon answered: %+v", r)
	}
	if res.Path != "/home/remote/notes.md" || string(res.Data) != "hello" || res.Size != 200 || res.EOF {
		t.Fatalf("result = %+v, want the remote machine's answer verbatim", res)
	}
	// The host is catway's own knowledge and never crosses the wire in the
	// reply, but the caller needs it: it is half the file's identity.
	if res.Host != testRemoteHost {
		t.Errorf("result host = %q, want %q", res.Host, testRemoteHost)
	}
}

// A cathost too old to transfer files is refused BY NAME, with the reason: an
// un-upgraded daemon is a different fix from an unreachable one.
func TestFileGetRefusesAHostThatCannotTransfer(t *testing.T) {
	o, _, remotePane, _, _ := twoHostOrch(t)

	r := &hostGuardResponder{}
	o.StartFileGet(r, app.FileGetParams{Pane: &remotePane, Path: "/etc/hosts"})
	if !r.fail {
		t.Fatalf("an incapable host should fail the command: %+v", r)
	}
	if !strings.Contains(r.errMsg, "transfer files") {
		t.Errorf("refusal does not say what could not be done: %q", r.errMsg)
	}
}

// An explicit host nobody is attached to is named rather than silently answered
// by whichever machine happened to be nearest.
func TestFileCommandsRefuseAnUnknownHost(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)

	r := &hostGuardResponder{}
	o.StartFileStat(r, app.FileStatParams{Path: "/etc/hosts", Host: "nowhere"})
	if !r.fail || !strings.Contains(r.errMsg, "unknown host nowhere") {
		t.Fatalf("unknown host: fail=%v msg=%q", r.fail, r.errMsg)
	}
}

// The anchor's cwd is only an anchor if it is on the SAME filesystem the
// operation will run on. A cwd from another box would resolve a relative path
// against a directory that does not exist there — worse than having no anchor,
// because it looks like an answer.
func TestFileAnchorDropsACwdFromAnotherMachine(t *testing.T) {
	o, localPane, remotePane, _, pdRemote := twoHostOrch(t)
	go o.run()
	o.hosts[testRemoteHost].setFeatures([]string{orchestration.FeatureFileTransfer})

	// Anchored on the LOCAL pane but addressed to the remote host: the local
	// pane's cwd must not travel.
	r := &hostGuardResponder{}
	syncPost(o, func() {
		o.StartFileStat(r, app.FileStatParams{Pane: &localPane, Host: testRemoteHost, Path: "notes.md"})
	})
	payload := pdRemote.expect(t, orchestration.MsgRequestFile)
	var req orchestration.RequestFile
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Req.Base != "" {
		t.Errorf("base = %q, want empty: it is a directory on a different machine", req.Req.Base)
	}

	// Anchored on a pane that IS on the addressed host: the cwd travels, because
	// there it means something.
	r2 := &hostGuardResponder{}
	syncPost(o, func() {
		o.StartFileStat(r2, app.FileStatParams{Pane: &remotePane, Path: "notes.md"})
	})
	payload = pdRemote.expect(t, orchestration.MsgRequestFile)
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Req.Base == "" {
		t.Error("base is empty for a pane on the addressed host; a relative path has nothing to resolve against")
	}
}

// A filesystem failure comes back INSIDE the result and becomes the command's
// error — it is the answer to the question that was asked. Only a lost round
// trip arrives as a transport failure.
func TestFileResultErrorFailsTheCommand(t *testing.T) {
	o, _, remotePane, _, pdRemote := twoHostOrch(t)
	go o.run()
	d := o.hosts[testRemoteHost]
	d.setFeatures([]string{orchestration.FeatureFileTransfer})

	r := &hostGuardResponder{}
	syncPost(o, func() {
		o.StartFilePut(r, app.FilePutParams{Pane: &remotePane, Path: "/etc/hosts", Data: []byte("x")})
	})
	payload := pdRemote.expect(t, orchestration.MsgRequestFile)
	var req orchestration.RequestFile
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Req.Op != filexfer.OpWrite || string(req.Req.Data) != "x" {
		t.Errorf("request = %+v", req.Req)
	}
	if req.Req.More || req.Req.Overwrite {
		t.Error("a one-shot put travelled as a chunk, or with permission to clobber")
	}

	d.dispatch(orchestration.MsgFileResult, mustJSON(t, orchestration.NewFileResult(req.ID, filexfer.OpResult{
		Path: "/etc/hosts", Error: "file exists (pass overwrite to replace it)",
	})))
	syncPost(o, func() {})

	if !r.fail || !strings.Contains(r.errMsg, "overwrite") {
		t.Fatalf("a refused write should fail the command with its reason: %+v", r)
	}
}

// Every chunk of a transfer is its own request, so several are legitimately
// outstanding against one machine — and they are matched by id, not by order,
// because the daemon runs each off its dispatch goroutine and answers whenever
// its disk does.
func TestFileRequestsAreMatchedByIdNotOrder(t *testing.T) {
	o, _, remotePane, _, pdRemote := twoHostOrch(t)
	go o.run()
	d := o.hosts[testRemoteHost]
	d.setFeatures([]string{orchestration.FeatureFileTransfer})

	first, second := &hostGuardResponder{}, &hostGuardResponder{}
	syncPost(o, func() {
		o.StartFileGet(first, app.FileGetParams{Pane: &remotePane, Path: "/a", Offset: 0, Length: 4})
		o.StartFileGet(second, app.FileGetParams{Pane: &remotePane, Path: "/b", Offset: 0, Length: 4})
	})
	var reqs []orchestration.RequestFile
	for range 2 {
		var req orchestration.RequestFile
		if err := json.Unmarshal(pdRemote.expect(t, orchestration.MsgRequestFile), &req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		reqs = append(reqs, req)
	}
	// Answered in the reverse order they were asked.
	d.dispatch(orchestration.MsgFileResult, mustJSON(t,
		orchestration.NewFileResult(reqs[1].ID, filexfer.OpResult{Path: "/b", Data: []byte("BBBB"), EOF: true})))
	d.dispatch(orchestration.MsgFileResult, mustJSON(t,
		orchestration.NewFileResult(reqs[0].ID, filexfer.OpResult{Path: "/a", Data: []byte("AAAA"), EOF: true})))
	syncPost(o, func() {})

	a, aOK := first.data.(app.FileGetResult)
	b, bOK := second.data.(app.FileGetResult)
	if !aOK || !bOK {
		t.Fatalf("results = %+v / %+v", first, second)
	}
	if string(a.Data) != "AAAA" || string(b.Data) != "BBBB" {
		t.Errorf("answers crossed: first got %q, second got %q", a.Data, b.Data)
	}
}

// A host that drops mid-transfer fails the outstanding chunk through the same
// machinery every other round trip uses, rather than leaving a `catctl cp` loop
// waiting for a reply that cannot come.
func TestFileRequestFailsWhenTheHostDrops(t *testing.T) {
	o, _, remotePane, _, pdRemote := twoHostOrch(t)
	go o.run()
	o.hosts[testRemoteHost].setFeatures([]string{orchestration.FeatureFileTransfer})

	r := &hostGuardResponder{}
	syncPost(o, func() {
		o.StartFileGet(r, app.FileGetParams{Pane: &remotePane, Path: "/a", Length: 16})
	})
	pdRemote.expect(t, orchestration.MsgRequestFile)

	syncPost(o, func() { o.flushPendingFor(testRemoteHost, "cathost connection lost") })
	if !r.fail || !strings.Contains(r.errMsg, "connection lost") {
		t.Fatalf("a dropped host should fail the chunk: %+v", r)
	}
}

// A local file takes the in-process path — the same filexfer.Do the daemon would
// run — rather than a round trip to the local cathost. It is what makes file
// transfer work against a local daemon too old to advertise the capability.
func TestFileGetOnTheLocalHostReadsTheDisk(t *testing.T) {
	o, localPane, _, pdLocal, _ := twoHostOrch(t)
	go o.run()

	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("local bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &syncedResponder{done: make(chan struct{})}
	syncPost(o, func() {
		o.StartFileGet(r, app.FileGetParams{Pane: &localPane, Path: path})
	})
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("a local file.get never resolved")
	}
	res, isRes := r.data.(app.FileGetResult)
	if !isRes || string(res.Data) != "local bytes" || !res.EOF {
		t.Fatalf("local read = %+v (err %q)", r.data, r.errMsg)
	}
	if res.Host != o.defaultHost {
		t.Errorf("result host = %q, want the local host %q", res.Host, o.defaultHost)
	}
	// Nothing was asked of the local cathost.
	if hasType(pdLocal.collect(150*time.Millisecond), orchestration.MsgRequestFile) {
		t.Error("a local file.get went out over the seam")
	}
}

// syncedResponder is hostGuardResponder for the asynchronous paths: it closes a
// channel when the command resolves, since a local read comes back on its own
// goroutine and then through the loop.
type syncedResponder struct {
	ok, fail bool
	data     any
	errMsg   string
	done     chan struct{}
}

func (*syncedResponder) WantsReply() bool { return true }
func (r *syncedResponder) OK(data any)    { r.ok, r.data = true, data; close(r.done) }
func (r *syncedResponder) Fail(msg string) {
	r.fail, r.errMsg = true, msg
	close(r.done)
}
