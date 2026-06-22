//go:build darwin

package detect

/*
#include <stdlib.h>
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

// ForegroundAgent returns the canonical agent label for the foreground process
// group of the terminal whose master fd is fd, or "" for a plain shell /
// unidentified program. Prefers the process-group leader, then any member.
func ForegroundAgent(fd uintptr) string {
	pgid := int(C.fg_pgrp(C.int(fd)))
	if pgid <= 0 {
		return ""
	}
	pids := make([]C.int, maxGroupPids)
	n := int(C.list_pgrp_pids(C.uint32_t(pgid), &pids[0], C.int(maxGroupPids)))
	if n <= 0 {
		return identifyPid(pgid) // fall back to the leader only
	}
	leader := ""
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
			return label // leader match wins outright
		}
		if leader == "" {
			leader = label
		}
	}
	return leader
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
