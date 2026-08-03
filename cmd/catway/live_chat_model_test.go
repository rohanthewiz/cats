//go:build ghostty

package main

import (
	"os"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/acpchat"
	"github.com/rohanthewiz/cats/internal/browserproto"
)

// A live end-to-end for the chat panel's model line: the real manager, a real
// `copilot --acp` subprocess, and the real on-disk history reader. The unit
// fleet fakes the agent and fakes the reader, so the one thing only this can
// answer is whether the poll schedule actually catches copilot's write.
//
// Skipped unless CATS_LIVE_COPILOT=1 — it spawns the agent, spends a Copilot
// turn, and needs the machine to be signed in.
//
// Measured here (copilot 1.0.77, 2026-08-03), and the reason modelPollDelays is
// a retry schedule rather than a single read:
//
//	+0.00s  status=starting
//	+2.22s  status=ready     (handshake done)
//	+6.53s  status=ready     (turn done — the read taken now finds nothing)
//	+6.93s  status=ready     model="gpt-5-mini · medium"  (the 400ms retry)
func TestLiveChatModel(t *testing.T) {
	if os.Getenv("CATS_LIVE_COPILOT") != "1" {
		t.Skip("set CATS_LIVE_COPILOT=1 to run against the real agent")
	}

	// The manager's threading contract is "everything on the owner's loop", so
	// the test brings a loop of its own — catway's, minus the orchestrator.
	loop := make(chan func(), 64)
	go func() {
		for f := range loop {
			f()
		}
	}()
	run := func(f func()) {
		done := make(chan struct{})
		loop <- func() { f(); close(done) }
		<-done
	}

	type stamped struct {
		st browserproto.ChatStateInfo
		at time.Duration
	}
	var states []stamped // appended from emit, which runs on the loop
	sent := time.Now()

	def := acpchat.Backends()[0]
	o := &orch{modelRoots: modelRootsFor()}
	m := acpchat.New(def, func(f func()) { loop <- f }, func(msg any) {
		if st, ok := msg.(browserproto.ChatState); ok {
			states = append(states, stamped{st.ChatStateInfo, time.Since(sent)})
		}
	})
	run(func() { m.SetModelResolver(o.chatModelResolver(def)) })
	defer run(func() { m.Shutdown() })

	cwd, _ := os.Getwd()
	run(func() { sent = time.Now(); m.Send(cwd, "Reply with just the word: ok") })

	var model string
	var at time.Duration
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) && model == "" {
		run(func() {
			for _, s := range states {
				if s.st.Model != "" {
					model, at = s.st.Model, s.at
				}
			}
		})
		time.Sleep(50 * time.Millisecond)
	}
	run(func() {
		for _, s := range states {
			t.Logf("  +%-8s status=%-8s model=%q",
				s.at.Round(10*time.Millisecond), s.st.Status, s.st.Model)
		}
	})
	if model == "" {
		t.Fatal("no model reached the clients")
	}
	t.Logf("model %q broadcast %s after the send", model, at.Round(10*time.Millisecond))
}
