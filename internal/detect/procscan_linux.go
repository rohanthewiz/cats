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

// ForegroundAgent returns the canonical agent label for the foreground process
// group of the terminal whose master fd is fd, or "" for a plain shell /
// unidentified program. Prefers the process-group leader, then any member.
func ForegroundAgent(fd uintptr) string {
	pgid := int(C.fg_pgrp(C.int(fd)))
	if pgid <= 0 {
		return ""
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return identifyPidLinux(pgid)
	}
	leader := ""
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
			return label
		}
		if leader == "" {
			leader = label
		}
	}
	return leader
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
