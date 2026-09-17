// Package agent decides whether a process was started by a coding agent.
//
// The test is ancestry, not the process itself: `npm run dev` launched inside
// Claude Code, Codex, Cursor or anything else shows the agent a few parents up.
// Matching on a name pattern keeps this agnostic — a new agent is one more word
// in the config, not a new integration.
package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const maxHops = 16

// Detector walks parent processes looking for a name that matches Pattern.
type Detector struct {
	Pattern *regexp.Regexp
}

// Spawned reports whether pid descends from a process matching the pattern.
//
// The listening process itself is deliberately not tested, only its ancestors.
// Otherwise any long-running daemon that merely carries an agent's name in its
// path — codex-health-daemon.py, a cursor helper — looks like a dev server
// someone just started.
func (d Detector) Spawned(pid int) bool {
	if d.Pattern == nil || pid <= 1 {
		return false
	}
	_, pid, ok := processInfo(pid)
	if !ok {
		return false
	}
	for hops := 0; pid > 1 && hops < maxHops; hops++ {
		cmd, ppid, ok := processInfo(pid)
		if !ok {
			return false
		}
		if d.Pattern.MatchString(cmd) {
			return true
		}
		pid = ppid
	}
	return false
}

// processInfo returns the command line and parent pid of a process.
func processInfo(pid int) (cmd string, ppid int, ok bool) {
	if runtime.GOOS == "linux" {
		return procfsInfo(pid)
	}
	out, err := exec.Command("ps", "-o", "ppid=,command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", 0, false
	}
	line := strings.TrimSpace(string(out))
	parent, rest, found := strings.Cut(line, " ")
	if !found {
		return "", 0, false
	}
	ppid, err = strconv.Atoi(strings.TrimSpace(parent))
	if err != nil {
		return "", 0, false
	}
	return strings.TrimSpace(rest), ppid, true
}

func procfsInfo(pid int) (string, int, bool) {
	dir := filepath.Join("/proc", strconv.Itoa(pid))
	cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return "", 0, false
	}
	status, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return "", 0, false
	}
	ppid := 0
	for _, line := range strings.Split(string(status), "\n") {
		if v, ok := strings.CutPrefix(line, "PPid:"); ok {
			ppid, _ = strconv.Atoi(strings.TrimSpace(v))
			break
		}
	}
	cmd := strings.ReplaceAll(strings.TrimRight(string(cmdline), "\x00"), "\x00", " ")
	return cmd, ppid, true
}
