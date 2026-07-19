package orchestration

import (
	"testing"
	"time"
)

func TestAgentPresenceAdoptsAndSwitches(t *testing.T) {
	var a agentPresence

	if !a.observeProcessProbe("claude") {
		t.Fatal("first identification should report a change")
	}
	if a.currentAgent() != "claude" {
		t.Fatalf("current = %q, want claude", a.currentAgent())
	}
	if a.observeProcessProbe("claude") {
		t.Fatal("same agent should not report a change")
	}
	if !a.observeProcessProbe("codex") {
		t.Fatal("switching agent should report a change")
	}
	if a.currentAgent() != "codex" {
		t.Fatalf("current = %q, want codex", a.currentAgent())
	}
}

// An identified agent survives transient misses and only clears on the Nth.
func TestAgentPresenceToleratesTransientMisses(t *testing.T) {
	var a agentPresence
	a.observeProcessProbe("claude")

	for i := 1; i < agentMissConfirmationAttempts; i++ {
		if a.observeProcessProbe("") {
			t.Fatalf("miss %d should not yet clear the agent", i)
		}
		if a.currentAgent() != "claude" {
			t.Fatalf("miss %d cleared agent prematurely", i)
		}
	}
	if !a.observeProcessProbe("") {
		t.Fatalf("miss %d should clear the agent", agentMissConfirmationAttempts)
	}
	if a.currentAgent() != "" {
		t.Fatal("agent should be cleared after confirmation attempts")
	}
}

// A non-empty probe resets the miss counter, so misses must be consecutive.
func TestAgentPresenceMissesMustBeConsecutive(t *testing.T) {
	var a agentPresence
	a.observeProcessProbe("claude")

	for range agentMissConfirmationAttempts - 1 {
		a.observeProcessProbe("")
	}
	if a.observeProcessProbe("claude") {
		t.Fatal("re-identifying the same agent is not a change")
	}
	// Counter reset: a single miss now must not clear.
	if a.observeProcessProbe("") {
		t.Fatal("miss counter should have reset after a hit")
	}
	if a.currentAgent() != "claude" {
		t.Fatal("agent should survive after counter reset")
	}
}

func TestAgentPresenceClear(t *testing.T) {
	var a agentPresence
	if a.clearCurrentAgent() {
		t.Fatal("clearing an empty presence is not a change")
	}
	a.observeProcessProbe("claude")
	if !a.clearCurrentAgent() {
		t.Fatal("clearing a present agent is a change")
	}
	if a.currentAgent() != "" {
		t.Fatal("agent should be empty after clear")
	}
}

func TestForegroundGroupChanged(t *testing.T) {
	if foregroundGroupChanged(noPGID, noPGID) {
		t.Fatal("absent→absent is not a change")
	}
	if !foregroundGroupChanged(42, noPGID) {
		t.Fatal("appearing group is a change")
	}
	if !foregroundGroupChanged(noPGID, 42) {
		t.Fatal("vanishing group is a change")
	}
	if !foregroundGroupChanged(7, 42) {
		t.Fatal("different group is a change")
	}
	if foregroundGroupChanged(42, 42) {
		t.Fatal("same group is not a change")
	}
}

func TestShouldProbeIdentifiedAgentRecheck(t *testing.T) {
	base := processProbeInput{
		currentAgentPresent: true,
		foregroundPgid:      42,
		lastForegroundPgid:  42,
		hasProcessProbe:     true,
		elapsedSinceCheck:   time.Second,
	}
	if shouldProbeForegroundJob(base) {
		t.Fatal("stable identified agent within recheck window should not probe")
	}

	due := base
	due.elapsedSinceCheck = processRecheckIdentified
	if !shouldProbeForegroundJob(due) {
		t.Fatal("identified agent past recheck interval should probe")
	}

	moved := base
	moved.foregroundPgid = 99
	if !shouldProbeForegroundJob(moved) {
		t.Fatal("process-group change should probe immediately")
	}
}

func TestShouldProbeUnidentified(t *testing.T) {
	// Never probed → must probe.
	if !shouldProbeForegroundJob(processProbeInput{foregroundPgid: 42}) {
		t.Fatal("first-ever probe should run")
	}

	// Probed, present group, stable, no agent → do not re-enumerate.
	stable := processProbeInput{
		foregroundPgid:     42,
		lastForegroundPgid: 42,
		hasProcessProbe:    true,
		elapsedSinceCheck:  time.Minute,
	}
	if shouldProbeForegroundJob(stable) {
		t.Fatal("stable unidentified present group should not keep enumerating")
	}

	// Missing foreground group, long elapsed → probe on the 30s timer.
	missing := processProbeInput{
		foregroundPgid:     noPGID,
		lastForegroundPgid: noPGID,
		hasProcessProbe:    true,
		elapsedSinceCheck:  processRecheckMissingForegroundGroup,
	}
	if !shouldProbeForegroundJob(missing) {
		t.Fatal("missing foreground group past 30s should probe")
	}
}

func TestShouldProbeAcquisitionWindow(t *testing.T) {
	in := processProbeInput{
		foregroundPgid:     42,
		lastForegroundPgid: 42,
		hasProcessProbe:    true,
		hasAcquisition:     true,
	}

	// Fast phase: probe every 500ms.
	fast := in
	fast.acquisitionAge = 500 * time.Millisecond
	fast.elapsedSinceCheck = processAcquisitionFastRecheck
	if !shouldProbeForegroundJob(fast) {
		t.Fatal("fast acquisition phase should probe at 500ms cadence")
	}
	tooSoon := fast
	tooSoon.elapsedSinceCheck = processAcquisitionFastRecheck - time.Millisecond
	if shouldProbeForegroundJob(tooSoon) {
		t.Fatal("fast acquisition phase should not probe before the interval")
	}

	// Slow phase: past the fast window, probe every 2s.
	slow := in
	slow.acquisitionAge = 3 * time.Second
	slow.elapsedSinceCheck = processAcquisitionSlowRecheck
	if !shouldProbeForegroundJob(slow) {
		t.Fatal("slow acquisition phase should probe at 2s cadence")
	}
	slowSoon := slow
	slowSoon.elapsedSinceCheck = processAcquisitionSlowRecheck - time.Millisecond
	if shouldProbeForegroundJob(slowSoon) {
		t.Fatal("slow acquisition phase should not probe before the interval")
	}

	// Past the acquisition window: the window no longer forces a probe (a stable
	// identified agent then falls back to the 5s recheck and stays quiet).
	expired := in
	expired.currentAgentPresent = true
	expired.acquisitionAge = processAcquisitionWindow + time.Second
	expired.elapsedSinceCheck = time.Second
	if shouldProbeForegroundJob(expired) {
		t.Fatal("expired acquisition window should not force a probe")
	}
}
