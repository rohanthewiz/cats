//go:build linux

package detect

/*
#include <unistd.h>
static int fg_pgrp(int fd) { return (int)tcgetpgrp(fd); }
*/
import "C"

import (
	"os"
	"strconv"
	"strings"
)

// ForegroundPGID returns the foreground process-group id of the terminal whose
// master fd is fd, or -1 if there is none. This is the cheap probe (a single
// tcgetpgrp) the detector uses to gate the far more expensive ForegroundAgent
// enumeration — see internal/orchestration/detectthrottle.go.
func ForegroundPGID(fd uintptr) int {
	pgid := int(C.fg_pgrp(C.int(fd)))
	if pgid <= 0 {
		return -1
	}
	return pgid
}

// ForegroundAgent returns the canonical agent label for the foreground process
// group of the terminal whose master fd is fd, or "" for a plain shell /
// unidentified program. Prefers the process-group leader, then any member.
func ForegroundAgent(fd uintptr) string {
	agent, _ := ForegroundAgentPIDs(fd)
	return agent
}

// ForegroundAgentPIDs is ForegroundAgent plus every pid in the group that
// identified as that agent, leader first. nil whenever the label is "".
//
// The pids are what turn "this pane runs claude" into "this pane runs *that*
// conversation": an agent keeps a registry of its live processes keyed by pid
// (AgentSessionID), and a pid is the only handle on it a terminal has. Without
// one, two panes running the same agent in the same directory are
// indistinguishable from the outside, and anything keyed on the directory alone
// answers both with whichever session wrote last.
//
// All of them rather than one, because the process that carries the *name* need
// not be the one that keeps the state: a shim or wrapper script called claude is
// what argv identifies, while the registry entry belongs to the real binary it
// exec'd or forked. The caller tries them in order and takes the first that
// answers.
func ForegroundAgentPIDs(fd uintptr) (string, []int) {
	pgid := int(C.fg_pgrp(C.int(fd)))
	if pgid <= 0 {
		return "", nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// /proc unreadable: the leader is the only pid we still hold.
		if label := identifyPidLinux(pgid); label != "" {
			return label, []int{pgid}
		}
		return "", nil
	}
	var agent string
	var matched []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if procPgrp(pid) != pgid {
			continue
		}
		label := identifyPidLinux(pid)
		if label == "" {
			continue
		}
		if pid == pgid {
			// The leader's label wins outright, and its pid leads the list.
			agent = label
			matched = append([]int{pid}, matched...)
			continue
		}
		if agent == "" {
			agent = label
		}
		matched = append(matched, pid)
	}
	return agent, matched
}

// ProcessCwd returns pid's current working directory, or "" when it cannot be
// read (a pid that has exited, one this process may not inspect, or a directory
// that has since been removed). Callers pass the pane's own PTY child — the
// shell whose `cd` moves the directory — because the terminal itself reports one
// only if the shell emits OSC 7, which most default shell setups do not.
func ProcessCwd(pid int) string {
	if pid <= 0 {
		return ""
	}
	dir, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	if err != nil || strings.HasSuffix(dir, " (deleted)") {
		return ""
	}
	return dir
}

// procPgrp reads the process group from /proc/<pid>/stat (field 5, after comm).
func procPgrp(pid int) int {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return -1
	}
	s := string(data)
	rparen := strings.LastIndexByte(s, ')') // comm may contain spaces/parens
	if rparen < 0 {
		return -1
	}
	fields := strings.Fields(s[rparen+1:]) // [0]=state [1]=ppid [2]=pgrp
	if len(fields) < 3 {
		return -1
	}
	pgrp, err := strconv.Atoi(fields[2])
	if err != nil {
		return -1
	}
	return pgrp
}

func identifyPidLinux(pid int) string {
	base := "/proc/" + strconv.Itoa(pid)
	cands := make([]string, 0, 8)
	if data, err := os.ReadFile(base + "/comm"); err == nil {
		cands = append(cands, strings.TrimSpace(string(data)))
	}
	if data, err := os.ReadFile(base + "/cmdline"); err == nil {
		for _, arg := range strings.Split(string(data), "\x00") {
			if arg != "" {
				cands = append(cands, arg)
			}
		}
	}
	return IdentifyFirst(cands...)
}
