// Package daemon polls the discovery sources and keeps the serve config in sync.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/breakzplatform/ts-autoserve/internal/agent"
	"github.com/breakzplatform/ts-autoserve/internal/config"
	"github.com/breakzplatform/ts-autoserve/internal/discover"
	"github.com/breakzplatform/ts-autoserve/internal/notify"
)

// Publisher is the part of the tailnet client the daemon needs.
type Publisher interface {
	Publish(ctx context.Context, port int) error
	Withdraw(ctx context.Context, port int) error
	Published(ctx context.Context) (map[int]bool, error)
	URL(ctx context.Context, port int) (string, error)
}

// Daemon watches local ports and mirrors them onto the tailnet.
type Daemon struct {
	Cfg       config.Config
	Sources   []discover.Source
	Pub       Publisher
	Notifiers []notify.Notifier
	DryRun    bool

	detector agent.Detector
	owned    map[int]*entry // ports this daemon published, by port
	started  bool
}

type entry struct {
	proc    string
	source  string
	missing int // consecutive polls without the port
}

// New returns a daemon ready to run.
func New(cfg config.Config, srcs []discover.Source, pub Publisher, ns []notify.Notifier) *Daemon {
	return &Daemon{
		Cfg:       cfg,
		Sources:   srcs,
		Pub:       pub,
		Notifiers: ns,
		detector:  agent.Detector{Pattern: cfg.Agent},
		owned:     map[int]*entry{},
	}
}

// Run polls until the context is cancelled, then withdraws what it published.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.reclaim(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(d.Cfg.Interval)
	defer ticker.Stop()
	for {
		if err := d.Poll(ctx); err != nil {
			slog.Error("poll failed", "err", err)
		}
		select {
		case <-ctx.Done():
			d.shutdown()
			return nil
		case <-ticker.C:
		}
	}
}

// reclaim drops mappings left behind by an earlier run. Anything still in the
// serve config at startup was either ours (the process died) or the user's; a
// port that is no longer listening cannot be the user's working setup, so it
// goes. Ports still in use are left alone and re-adopted on the first poll.
func (d *Daemon) reclaim(ctx context.Context) error {
	published, err := d.Pub.Published(ctx)
	if err != nil {
		return err
	}
	live, err := d.candidates(ctx)
	if err != nil {
		return err
	}
	for port := range published {
		if _, ok := live[port]; ok {
			continue
		}
		if d.DryRun {
			slog.Info("would withdraw stale mapping", "port", port)
			continue
		}
		if err := d.Pub.Withdraw(ctx, port); err != nil {
			slog.Warn("withdraw failed", "port", port, "err", err)
			continue
		}
		slog.Info("withdrew stale mapping", "port", port)
	}
	return nil
}

// Poll runs one reconciliation pass.
func (d *Daemon) Poll(ctx context.Context) error {
	live, err := d.candidates(ctx)
	if err != nil {
		return err
	}

	var fresh []int
	for port, l := range live {
		if e, ok := d.owned[port]; ok {
			e.missing = 0
			continue
		}
		if d.DryRun {
			slog.Info("would publish", "port", port, "proc", l.Proc, "source", l.Source)
			continue
		}
		if err := d.Pub.Publish(ctx, port); err != nil {
			slog.Error("publish failed", "port", port, "err", err)
			continue
		}
		d.owned[port] = &entry{proc: l.Proc, source: l.Source}
		fresh = append(fresh, port)
		url, _ := d.Pub.URL(ctx, port)
		slog.Info("published", "port", port, "proc", l.Proc, "url", url)
		// The first pass adopts whatever was already running; announcing each
		// one separately would mean a burst of messages on every restart.
		if d.started {
			notify.All(ctx, d.Notifiers, notify.Event{
				Kind: "up", Port: port, URL: url, Proc: l.Proc, Source: l.Source,
				Text: fmt.Sprintf("%s up on port %d\n%s", label(l.Proc), port, url),
			})
		}
	}

	for port, e := range d.owned {
		if _, ok := live[port]; ok {
			continue
		}
		e.missing++
		if e.missing < d.Cfg.Grace {
			continue
		}
		if err := d.Pub.Withdraw(ctx, port); err != nil {
			slog.Error("withdraw failed", "port", port, "err", err)
			continue
		}
		delete(d.owned, port)
		slog.Info("withdrew", "port", port)
		notify.All(ctx, d.Notifiers, notify.Event{
			Kind: "down", Port: port, Proc: e.proc, Source: e.source,
			Text: fmt.Sprintf("port %d is gone", port),
		})
	}

	if !d.started {
		d.started = true
		if len(fresh) > 0 {
			sort.Ints(fresh)
			notify.All(ctx, d.Notifiers, notify.Event{
				Kind: "start", Text: "ts-autoserve is up; already serving " + join(fresh),
			})
		}
	}
	return nil
}

// shutdown withdraws every mapping this run created, so a stopped daemon does
// not leave the tailnet serving ports nobody is watching.
func (d *Daemon) shutdown() {
	if len(d.owned) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for port := range d.owned {
		if err := d.Pub.Withdraw(ctx, port); err != nil {
			slog.Warn("withdraw on shutdown failed", "port", port, "err", err)
		}
	}
}

// candidates asks every source what is listening and keeps what the policy wants.
func (d *Daemon) candidates(ctx context.Context) (map[int]discover.Listener, error) {
	out := map[int]discover.Listener{}
	var firstErr error
	for _, src := range d.Sources {
		ls, err := src.Listeners(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("source %s: %w", src.Name(), err)
			}
			continue
		}
		for _, l := range ls {
			if !d.wanted(l) {
				continue
			}
			if _, dup := out[l.Port]; !dup {
				out[l.Port] = l
			}
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// wanted applies the mode. Exclusions win over everything: a port listed there
// is never published, however it was started.
func (d *Daemon) wanted(l discover.Listener) bool {
	if d.Cfg.Exclude.Has(l.Port) {
		return false
	}
	switch d.Cfg.Mode {
	case config.ModeDev:
		return d.Cfg.Dev.Has(l.Port)
	case config.ModeAgent:
		return d.agentSpawned(l)
	case config.ModeAll:
		return d.Cfg.Range.Has(l.Port)
	default:
		return d.Cfg.Dev.Has(l.Port) || d.agentSpawned(l)
	}
}

func (d *Daemon) agentSpawned(l discover.Listener) bool {
	if l.PID == 0 {
		return false
	}
	return d.detector.Spawned(l.PID)
}

func label(proc string) string {
	if proc == "" {
		return "a local server"
	}
	return proc
}

func join(ports []int) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = fmt.Sprint(p)
	}
	return strings.Join(parts, ", ")
}
