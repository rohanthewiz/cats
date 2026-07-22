package orchestration

// Stage C — process-probe throttle. The detection loop must know which agent owns
// a pane's foreground, but enumerating the foreground process group (per-pid comm,
// exec path, and argv via sysctl) is far more expensive than the 300ms loop cadence
// warrants for an idle pane. This file ports cats's throttle (Rust:
// src/pane.rs — should_probe_foreground_job + AgentDetectionPresence) so the Go
// backend only runs that enumeration when it can pay off:
//
//   - a cheap tcgetpgrp (detect.ForegroundPGID) runs every tick; the full probe
//     runs only when the foreground process group changed or a recheck interval
//     elapsed (5s once an agent is identified, 30s while a foreground group is
//     missing entirely).
//   - right after an unidentified process-group change, an acquisition window
//     probes faster (500ms for the first 1.5s, then 2s, up to 8s) to catch a
//     just-launched agent that is still revealing its identity (agents run under
//     `node`, so argv settles a beat after the group appears).
//   - an identified agent is not dropped on a single miss: it takes 6 consecutive
//     misses (AGENT_MISS_CONFIRMATION_ATTEMPTS) to clear, so a transient probe
//     hiccup doesn't flap identity.
//
// Scope note: cats's throttle also threads suppressed-agent (remote release),
// pending foreground-shell-exit, and full-lifecycle-authority inputs. Those
// subsystems don't exist on the Go backend yet, so this port covers the
// no-suppression / no-lifecycle-authority subset and omits those branches.
//
// These helpers are pure (no ghostty build tag) so they unit-test without the
// emulator toolchain.

import "time"

// Throttle tuning, matched to cats's src/pane.rs constants.
const (
	agentMissConfirmationAttempts        = 6
	processRecheckIdentified             = 5 * time.Second
	processRecheckMissingForegroundGroup = 30 * time.Second
	processAcquisitionWindow             = 8 * time.Second
	processAcquisitionFastWindow         = 1500 * time.Millisecond
	processAcquisitionFastRecheck        = 500 * time.Millisecond
	processAcquisitionSlowRecheck        = 2 * time.Second
)

// noPGID is the sentinel for "no foreground process group" — both detect.ForegroundPGID
// and the throttle use it so a present group (>0) is never confused with absence.
const noPGID = -1

// agentPresence debounces process-based identity: an identified agent survives a
// few empty probes before it's cleared. Ports cats's AgentDetectionPresence.
type agentPresence struct {
	current string
	misses  int
}

func (a *agentPresence) currentAgent() string { return a.current }

func (a *agentPresence) clearCurrentAgent() bool {
	if a.current == "" {
		a.misses = 0
		return false
	}
	a.current = ""
	a.misses = 0
	return true
}

// observeProcessProbe folds one probe result into the presence and reports whether
// the effective agent changed. A non-empty result adopts immediately; an empty
// result only clears after agentMissConfirmationAttempts consecutive misses.
func (a *agentPresence) observeProcessProbe(identified string) bool {
	if identified != "" {
		a.misses = 0
		if identified == a.current {
			return false
		}
		a.current = identified
		return true
	}
	if a.current == "" {
		a.misses = 0
		return false
	}
	a.misses++
	if a.misses < agentMissConfirmationAttempts {
		return false
	}
	a.current = ""
	a.misses = 0
	return true
}

// foregroundGroupChanged reports whether the foreground process group id differs
// from the last observed one, treating noPGID as a real "absent" value (so an
// appearing or vanishing group counts as a change, but absent→absent does not).
// Ports cats's foreground_group_changed.
func foregroundGroupChanged(pgid, last int) bool {
	return pgid != last && (pgid > 0 || last > 0)
}

// processProbeInput is the throttle's decision input for one tick.
type processProbeInput struct {
	currentAgentPresent bool
	foregroundPgid      int
	lastForegroundPgid  int
	hasProcessProbe     bool          // have we ever run the full enumeration?
	hasAcquisition      bool          // is an acquisition window active?
	acquisitionAge      time.Duration // how long the acquisition window has run
	elapsedSinceCheck   time.Duration // since the last full enumeration
}

// shouldProbeForegroundJob decides whether this tick should run the expensive
// foreground enumeration. Ports the no-suppression subset of cats's
// should_probe_foreground_job.
func shouldProbeForegroundJob(in processProbeInput) bool {
	changed := foregroundGroupChanged(in.foregroundPgid, in.lastForegroundPgid)

	if in.hasAcquisition {
		interval := processAcquisitionSlowRecheck
		if in.acquisitionAge <= processAcquisitionFastWindow {
			interval = processAcquisitionFastRecheck
		}
		if in.acquisitionAge <= processAcquisitionWindow && in.elapsedSinceCheck >= interval {
			return true
		}
	}

	if !in.currentAgentPresent {
		return !in.hasProcessProbe ||
			changed ||
			(in.foregroundPgid <= 0 && in.elapsedSinceCheck >= processRecheckMissingForegroundGroup)
	}

	return changed || in.elapsedSinceCheck >= processRecheckIdentified
}
