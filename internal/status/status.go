// Package status builds the report behind `ts-autoserve status`.
//
// The report is read-only: it describes what tailscaled publishes, which local
// process answers behind each port, and whether the user service runs. It
// never touches the serve config. The JSON form is a contract for scripts and
// agents: fields may be added, but a field is never removed or changed in
// meaning without bumping Version.
package status

import (
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

// Version is the schema version of the JSON report.
const Version = 1

// Report is everything `status` knows.
type Report struct {
	Version   int        `json:"version"`
	Node      string     `json:"node"`
	Endpoints []Endpoint `json:"endpoints"`
	Service   Service    `json:"service"`
}

// Endpoint is one HTTPS port tailscaled serves on this node.
type Endpoint struct {
	Port int    `json:"port"`
	URL  string `json:"url"`
	// Proxy is where the port forwards to, empty for a mapping that is not a
	// plain reverse proxy.
	Proxy string `json:"proxy"`
	// Managed is true when ts-autoserve published the port, false for a
	// mapping set up by hand.
	Managed bool `json:"managed"`
	// Process is what listens on the local port the mapping forwards to. Nil
	// when nothing local listens there: the server is stopped, or the mapping
	// does not point at a loopback port.
	Process *Process `json:"process"`
}

// Process is a local process listening on a port.
type Process struct {
	// PID is 0 when the OS does not say who owns the socket, as ss does for
	// another user's process.
	PID int `json:"pid"`
	// Name is the command name as lsof or ss report it, empty when unknown.
	Name string `json:"name"`
}

// Service is the state of the OS user service.
type Service struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Detail    string `json:"detail"`
	File      string `json:"file"`
}

// Published is a port served over HTTPS and where it forwards to.
type Published struct {
	Port  int
	Proxy string
}

// Build assembles a report. managed is the set of ports the daemon published;
// listening maps each local listening port to its process, as the daemon's own
// discovery reports it.
func Build(node string, published []Published, managed map[int]bool, listening map[int]Process, svc Service) Report {
	endpoints := make([]Endpoint, 0, len(published))
	for _, p := range published {
		e := Endpoint{
			Port:    p.Port,
			URL:     fmt.Sprintf("https://%s:%d/", node, p.Port),
			Proxy:   p.Proxy,
			Managed: managed[p.Port],
		}
		if target, ok := LocalTarget(p.Proxy); ok {
			if proc, ok := listening[target]; ok {
				e.Process = &proc
			}
		}
		endpoints = append(endpoints, e)
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].Port < endpoints[j].Port })
	return Report{Version: Version, Node: node, Endpoints: endpoints, Service: svc}
}

// LocalTarget reports the local port a proxy target points at. The daemon
// always proxies to http://127.0.0.1:<port>, but a mapping made by hand can
// forward a served port to a different local one, or to another machine,
// which has no local process to report.
func LocalTarget(proxy string) (int, bool) {
	if proxy == "" {
		return 0, false
	}
	if !strings.Contains(proxy, "://") {
		proxy = "http://" + proxy
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	host := u.Hostname()
	if host == "localhost" {
		return port, true
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Unmap().IsLoopback() {
		return 0, false
	}
	return port, true
}

// WriteJSON prints the report as indented JSON.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText prints the report for a person.
func WriteText(w io.Writer, r Report) {
	fmt.Fprintf(w, "node: %s\n", r.Node)
	state := "not installed"
	switch {
	case r.Service.Installed && r.Service.Running:
		state = "running"
	case r.Service.Installed:
		state = "installed, not running"
	}
	fmt.Fprintf(w, "service: %s (%s)\n", state, r.Service.Detail)
	if len(r.Endpoints) == 0 {
		fmt.Fprintln(w, "published: nothing")
		return
	}
	fmt.Fprintln(w, "published:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, e := range r.Endpoints {
		origin := "manual"
		if e.Managed {
			origin = "ts-autoserve"
		}
		target := e.Proxy
		if target == "" {
			target = "-"
		}
		fmt.Fprintf(tw, "  %s\t->  %s\t(%s)\t%s\n", e.URL, strings.TrimPrefix(target, "http://"), origin, describe(e.Process))
	}
	tw.Flush()
}

// describe names the process behind an endpoint in a few words.
func describe(p *Process) string {
	switch {
	case p == nil:
		return "not listening"
	case p.PID != 0 && p.Name != "":
		return p.Name + " (pid " + strconv.Itoa(p.PID) + ")"
	case p.PID != 0:
		return "pid " + strconv.Itoa(p.PID)
	case p.Name != "":
		return p.Name
	default:
		return "listening, owner unknown"
	}
}
