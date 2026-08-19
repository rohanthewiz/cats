//go:build darwin

package detect

/*
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <libproc.h>
#include <sys/sysctl.h>

static int fg_pgrp(int fd) {
    return (int)tcgetpgrp(fd);
}

// list_pgrp_pids fills pids[] with the pids in process group pgid; returns count.
static int list_pgrp_pids(uint32_t pgid, int *pids, int maxpids) {
    int n = proc_listpids(PROC_PGRP_ONLY, pgid, pids, maxpids * (int)sizeof(int));
    if (n <= 0) return 0;
    return n / (int)sizeof(int);
}

static int proc_comm(int pid, char *buf, int size) {
    return proc_name(pid, buf, (uint32_t)size);
}

static int proc_path(int pid, char *buf, int size) {
    return proc_pidpath(pid, buf, (uint32_t)size);
}

// proc_cwd copies pid's current working directory into buf; returns its length
// (0 on failure — a dead pid, or a path that does not fit).
static int proc_cwd(int pid, char *buf, int size) {
    struct proc_vnodepathinfo vpi;
    if (proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, 0, &vpi, sizeof(vpi)) <= 0) return 0;
    size_t len = strlen(vpi.pvi_cdir.vip_path);
    if (len == 0 || (int)len >= size) return 0;
    memcpy(buf, vpi.pvi_cdir.vip_path, len + 1);
    return (int)len;
}

// proc_args fetches KERN_PROCARGS2 for pid into buf; returns bytes written (0 on failure).
static int proc_args(int pid, char *buf, int size) {
    int mib[3] = { CTL_KERN, KERN_PROCARGS2, pid };
    size_t len = (size_t)size;
    if (sysctl(mib, 3, buf, &len, NULL, 0) != 0) return 0;
    return (int)len;
}
*/
import "C"

import "unsafe"

const (
	maxGroupPids = 256
	pathBufSize  = 4096
	argsBufSize  = 1 << 16
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
	pids := make([]C.int, maxGroupPids)
	n := int(C.list_pgrp_pids(C.uint32_t(pgid), &pids[0], C.int(maxGroupPids)))
	if n <= 0 {
		// Enumeration failed: the leader is the only pid we still hold.
		if label := identifyPid(pgid); label != "" {
			return label, []int{pgid}
		}
		return "", nil
	}
	var agent string
	var matched []int
	for i := 0; i < n && i < maxGroupPids; i++ {
		pid := int(pids[i])
		if pid == 0 {
			continue
		}
		label := identifyPid(pid)
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
// read (a pid that has exited, or one this process may not inspect). Callers
// pass the pane's own PTY child — the shell whose `cd` moves the directory —
// because the terminal itself reports one only if the shell emits OSC 7, which
// most default shell setups do not.
func ProcessCwd(pid int) string {
	if pid <= 0 {
		return ""
	}
	buf := make([]C.char, pathBufSize)
	if n := C.proc_cwd(C.int(pid), &buf[0], C.int(len(buf))); n <= 0 {
		return ""
	}
	return C.GoString(&buf[0])
}

// identifyPid checks a process's comm, exec-path basename, and argv for an agent.
func identifyPid(pid int) string {
	cands := make([]string, 0, 8)
	if s := procComm(pid); s != "" {
		cands = append(cands, s)
	}
	if s := procPath(pid); s != "" {
		cands = append(cands, s)
	}
	cands = append(cands, procArgv(pid)...)
	return IdentifyFirst(cands...)
}

func procComm(pid int) string {
	buf := make([]C.char, pathBufSize)
	if n := C.proc_comm(C.int(pid), &buf[0], C.int(len(buf))); n <= 0 {
		return ""
	}
	return C.GoString(&buf[0])
}

func procPath(pid int) string {
	buf := make([]C.char, pathBufSize)
	if n := C.proc_path(C.int(pid), &buf[0], C.int(len(buf))); n <= 0 {
		return ""
	}
	return C.GoString(&buf[0])
}

// procArgv parses KERN_PROCARGS2: [int32 argc][exec_path\0][padding\0..][argv0\0 argv1\0 ...].
func procArgv(pid int) []string {
	buf := make([]C.char, argsBufSize)
	n := int(C.proc_args(C.int(pid), &buf[0], C.int(argsBufSize)))
	if n < 4 {
		return nil
	}
	raw := C.GoBytes(unsafe.Pointer(&buf[0]), C.int(n))
	argc := int(int32(uint32(raw[0]) | uint32(raw[1])<<8 | uint32(raw[2])<<16 | uint32(raw[3])<<24))
	if argc <= 0 {
		return nil
	}
	p := 4
	for p < len(raw) && raw[p] != 0 { // skip exec_path
		p++
	}
	for p < len(raw) && raw[p] == 0 { // skip null padding
		p++
	}
	args := make([]string, 0, argc)
	for p < len(raw) && len(args) < argc {
		start := p
		for p < len(raw) && raw[p] != 0 {
			p++
		}
		args = append(args, string(raw[start:p]))
		p++ // skip terminating null
	}
	return args
}
