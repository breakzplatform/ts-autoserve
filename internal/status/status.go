// Package status builds the report behind `ts-autoserve status`.
//
// The report is read-only: it describes what tailscaled publishes and whether
// the user service runs, and never touches the serve config. The JSON form is
// a contract for scripts and agents, so its shape changes only with Version.
package status

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
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

// Build assembles a report. managed is the set of ports the daemon published.
func Build(node string, published []Published, managed map[int]bool, svc Service) Report {
	endpoints := make([]Endpoint, 0, len(published))
	for _, p := range published {
		endpoints = append(endpoints, Endpoint{
			Port:    p.Port,
			URL:     fmt.Sprintf("https://%s:%d/", node, p.Port),
			Proxy:   p.Proxy,
			Managed: managed[p.Port],
		})
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].Port < endpoints[j].Port })
	return Report{Version: Version, Node: node, Endpoints: endpoints, Service: svc}
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
	for _, e := range r.Endpoints {
		origin := "manual"
		if e.Managed {
			origin = "ts-autoserve"
		}
		target := e.Proxy
		if target == "" {
			target = "-"
		}
		fmt.Fprintf(w, "  %s  ->  %s  (%s)\n", e.URL, strings.TrimPrefix(target, "http://"), origin)
	}
}
