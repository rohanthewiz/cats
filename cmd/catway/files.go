//go:build ghostty

package main

import (
	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/filexfer"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// file.stat / file.get / file.put (app.Backend seam): reading and writing files
// on whichever machine a pane runs on.
//
// The rule is the one every per-machine command in this slice follows — a
// question about another machine is answered by that machine — and here it is
// not a preference but the only possible answer: the bytes are on that disk.
//
// The three commands share one shape, so they share one runner. Each resolves
// two things on the loop goroutine (which host, and what a relative path
// anchors against), builds a filexfer.OpRequest, and hands it to runFileOp.
// The disk work never touches the orchestrator's goroutine at either end.
//
// Local files take the in-process path rather than a round trip to the local
// cathost. That is the path.list arrangement rather than the worktree one, and
// the difference is what "one implementation" costs in each case: a worktree
// operation is a sequence of git invocations, so it has to run in exactly one
// place, while a file operation IS internal/filexfer — the same function the
// daemon would call, called directly. It also means file transfer works against
// a local cathost too old to advertise the capability.

// StartFileStat answers "what is at this path" on the addressed machine.
func (o *orch) StartFileStat(r app.Responder, p app.FileStatParams) {
	hostID, base, ok := o.fileAnchor(r, p.Pane, p.Host)
	if !ok {
		return
	}
	o.runFileOp(r, hostID, filexfer.OpRequest{Op: filexfer.OpStat, Path: p.Path, Base: base},
		func(res filexfer.OpResult) {
			if res.Error != "" {
				r.Fail(res.Error)
				return
			}
			r.OK(app.FileStatResult{
				Path: res.Path, Host: hostID, Size: res.Size,
				Mode: res.Mode, Dir: res.Dir, MTime: res.MTime,
			})
		})
}

// StartFileGet reads one slice of a file. The length policy — including the
// refusal that makes a whole-file read of a large file an error rather than a
// silent prefix — lives in filexfer and therefore applies identically to a
// local file and a remote one.
func (o *orch) StartFileGet(r app.Responder, p app.FileGetParams) {
	hostID, base, ok := o.fileAnchor(r, p.Pane, p.Host)
	if !ok {
		return
	}
	o.runFileOp(r, hostID, filexfer.OpRequest{
		Op: filexfer.OpRead, Path: p.Path, Base: base,
		Offset: p.Offset, Length: p.Length,
	}, func(res filexfer.OpResult) {
		if res.Error != "" {
			r.Fail(res.Error)
			return
		}
		r.OK(app.FileGetResult{
			Path: res.Path, Host: hostID, Size: res.Size,
			Offset: res.Offset, Data: res.Data, EOF: res.EOF,
		})
	})
}

// StartFilePut writes one slice of a file.
func (o *orch) StartFilePut(r app.Responder, p app.FilePutParams) {
	hostID, base, ok := o.fileAnchor(r, p.Pane, p.Host)
	if !ok {
		return
	}
	o.runFileOp(r, hostID, filexfer.OpRequest{
		Op: filexfer.OpWrite, Path: p.Path, Base: base,
		Offset: p.Offset, Data: p.Data, More: p.More,
		Mode: p.Mode, Overwrite: p.Overwrite,
	}, func(res filexfer.OpResult) {
		if res.Error != "" {
			r.Fail(res.Error)
			return
		}
		r.OK(app.FilePutResult{
			Path: res.Path, Host: hostID, Written: res.Written,
			Complete: res.Complete, Size: res.Size,
		})
	})
}

// fileAnchor resolves the two things a file command needs before it can run:
// which machine, and what a relative path resolves against there. Reports false
// (having already failed r) for a host nobody is attached to. Loop-goroutine
// only.
//
// The base is dropped when the anchor pane is on a different machine than the
// one being asked — the path.list rule, and for the same reason: a cwd from
// another filesystem is not an anchor, it is a plausible-looking wrong answer.
// With no base, filexfer.Expand leaves a relative path relative and the
// answering machine resolves it against its own process cwd, which is at least
// a directory that exists there.
func (o *orch) fileAnchor(r app.Responder, pane *uint32, host string) (hostID, base string, ok bool) {
	anchor := o.anchorPane(pane)
	hostID = host
	if hostID == "" {
		hostID = o.paneHostID(anchor)
	} else if hostID != localHostID && o.hosts[hostID] == nil {
		r.Fail(unknownHostListErr(hostID))
		return "", "", false
	}
	if o.paneHostID(anchor) == hostID {
		base = o.anchorPaneCwd(pane)
	}
	return hostID, base, true
}

// runFileOp runs one operation on the machine that owns the file and calls done
// with its result, back on the loop goroutine. Loop-goroutine only.
func (o *orch) runFileOp(r app.Responder, hostID string, req filexfer.OpRequest, done func(filexfer.OpResult)) {
	if hostID == localHostID {
		// Off the loop: this catway's own disk can be a network mount too, and
		// the loop goroutine is what every pane's input passes through.
		go func() {
			res := filexfer.Do(req)
			o.post(func() { done(res) })
		}()
		return
	}
	d := o.hostByID(hostID)
	if !d.supports(orchestration.FeatureFileTransfer) {
		r.Fail(o.hostCapabilityErr(hostID, "transfer files", orchestration.FeatureFileTransfer))
		return
	}
	o.nextFileReq++
	id := o.nextFileReq
	// Registered before the send, so a host that drops mid-transfer fails this
	// chunk through flushPendingFor rather than leaving a `catctl cp` loop
	// waiting for a reply that cannot come.
	o.registerPending(fileResponder{r: r, done: done}, fileKey(hostID, id))
	d.send(orchestration.NewRequestFile(id, req))
}

// fileResponder turns the daemon's OpResult into the command's answer on the way
// back, the way worktreeResponder does: the pending queue carries one Responder
// and knows nothing about the shape of what it is resolving, while what a file
// result MEANS to each of the three commands is entirely catway's.
type fileResponder struct {
	r    app.Responder
	done func(filexfer.OpResult)
}

func (f fileResponder) WantsReply() bool { return f.r.WantsReply() }

func (f fileResponder) OK(data any) {
	res, ok := data.(filexfer.OpResult)
	if !ok {
		f.r.Fail("bad file reply")
		return
	}
	f.done(res)
}

// Fail is a transport failure — a dropped host or a timeout. A filesystem
// failure never arrives this way: it comes back inside the result, because a
// missing file is an answer rather than a lost question.
func (f fileResponder) Fail(msg string) { f.r.Fail(msg) }
