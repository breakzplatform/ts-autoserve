// Package discover finds local services worth publishing on the tailnet.
//
// A Source answers one question: what is listening right now? The host source
// reads the kernel's socket table; later sources (Docker, Podman) map their own
// published ports onto the same shape, so the daemon never learns where a
// service came from.
package discover

import "context"

// Listener is one local service found by a Source.
type Listener struct {
	Port   int    // local TCP port
	PID    int    // owning process, 0 when the source cannot tell
	Proc   string // process or container name, for the notification text
	Source string // "host", "docker", ...
	Label  string // optional human label (container name, project dir)
}

// Source reports the services currently listening.
type Source interface {
	Name() string
	Listeners(ctx context.Context) ([]Listener, error)
}
