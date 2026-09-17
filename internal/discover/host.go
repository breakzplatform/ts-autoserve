package discover

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// Host reads listening TCP sockets from the OS: lsof on macOS/BSD, ss on Linux.
//
// Only loopback and wildcard binds count. A server bound to one LAN address is
// deliberately not ours to publish.
type Host struct{}

func (Host) Name() string { return "host" }

func (h Host) Listeners(ctx context.Context) ([]Listener, error) {
	switch runtime.GOOS {
	case "linux":
		return h.fromSS(ctx)
	default:
		return h.fromLsof(ctx)
	}
}

// lsof -nP -iTCP -sTCP:LISTEN, one row per socket:
//
//	node  4615 dev  23u  IPv4 0x... 0t0 TCP 127.0.0.1:4615 (LISTEN)
func (Host) fromLsof(ctx context.Context) ([]Listener, error) {
	out, err := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN").Output()
	if err != nil {
		// lsof exits 1 when nothing matches; that is not a failure.
		if ee, ok := err.(*exec.ExitError); ok && len(out) == 0 && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("lsof: %w", err)
	}
	seen := map[int]Listener{}
	for _, line := range strings.Split(string(out), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 9 {
			continue
		}
		port, ok := localPort(f[8])
		if !ok {
			continue
		}
		if _, dup := seen[port]; dup {
			continue
		}
		pid, _ := strconv.Atoi(f[1])
		seen[port] = Listener{Port: port, PID: pid, Proc: f[0], Source: "host"}
	}
	return values(seen), nil
}

// ss -lntpH, one row per socket:
//
//	LISTEN 0 511 127.0.0.1:5173 0.0.0.0:* users:(("node",pid=812,fd=23))
func (Host) fromSS(ctx context.Context) ([]Listener, error) {
	out, err := exec.CommandContext(ctx, "ss", "-lntpH").Output()
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}
	seen := map[int]Listener{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		port, ok := localPort(f[3])
		if !ok {
			continue
		}
		if _, dup := seen[port]; dup {
			continue
		}
		l := Listener{Port: port, Source: "host"}
		if m := ssProc.FindStringSubmatch(line); m != nil {
			l.Proc = m[1]
			l.PID, _ = strconv.Atoi(m[2])
		}
		seen[port] = l
	}
	return values(seen), nil
}

var ssProc = regexp.MustCompile(`users:\(\("([^"]+)",pid=(\d+)`)

// localPort accepts the address forms that mean "reachable on this machine":
// wildcard (*:3000, 0.0.0.0:3000, [::]:3000) and loopback (127.0.0.1, [::1]).
// The parsing is left to the standard library, so an IPv6 address carrying an
// interface zone -- [::1%lo0], which macOS lsof writes -- still counts.
func localPort(addr string) (int, bool) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	switch host {
	case "", "*", "localhost": // wildcard as lsof and ss spell it
		return port, true
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i] // drop the zone: ::1%lo0 -> ::1
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return 0, false
	}
	// Unmap first, so ::ffff:127.0.0.1 answers IsLoopback.
	ip = ip.Unmap()
	if !ip.IsLoopback() && !ip.IsUnspecified() {
		return 0, false
	}
	return port, true
}

func values(m map[int]Listener) []Listener {
	out := make([]Listener, 0, len(m))
	for _, l := range m {
		out = append(out, l)
	}
	return out
}
