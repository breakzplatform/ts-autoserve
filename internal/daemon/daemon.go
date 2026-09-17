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
	Publish(ctx context.Context, ports []int) error
	Withdraw(ctx context.Context, ports []int) error
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
	foreign  map[int]bool   // published by someone else: never ours to touch
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
		foreign:   map[int]bool{},
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

// reclaim sorts out what is already in the serve config at startup. A port
// that is no longer listening cannot be anybody's working setup, so it goes:
// it is leftover from a run of this daemon that died. A port that is still
// listening stays, and is remembered as foreign.
//
// ipn.ServeConfig records no author, so a live mapping we did not make this
// run is indistinguishable from one the user typed by hand. The daemon
// therefore never publishes over it and never withdraws it -- otherwise a
// `tailscale serve 3000` set up by hand would be adopted on the first poll and
// silently deleted the moment that server stopped.
func (d *Daemon) reclaim(ctx context.Context) error {
	published, err := d.Pub.Published(ctx)
	if err != nil {
		return err
	}
	// Every listening port, not just the ones policy would publish: a mapping
	// the user made for a port outside dev range is still theirs.
	listening, err := d.listening(ctx)
	if err != nil {
		return err
	}
	var stale []int
	for port := range published {
		if listening[port] {
			d.foreign[port] = true
			continue
		}
		stale = append(stale, port)
	}
	sort.Ints(stale)
	if len(stale) == 0 {
		return nil
	}
	if d.DryRun {
		slog.Info("would withdraw stale mappings", "ports", stale)
		return nil
	}
	if err := d.Pub.Withdraw(ctx, stale); err != nil {
		slog.Warn("withdraw failed", "ports", stale, "err", err)
		return nil
	}
	slog.Info("withdrew stale mappings", "ports", stale)
	return nil
}

// Poll runs one reconciliation pass.
func (d *Daemon) Poll(ctx context.Context) error {
	live, err := d.candidates(ctx)
	if err != nil {
		return err
	}

	fresh := d.publishNew(ctx, live)
	d.withdrawGone(ctx, live)

	if !d.started {
		d.started = true
		if len(fresh) > 0 {
			notify.All(ctx, d.Notifiers, notify.Event{
				Kind: "start", Text: "ts-autoserve is up; already serving " + join(fresh),
			})
		}
	}
	return nil
}

// publishNew publishes the candidates we do not already own, in one batch, and
// returns the ports that went up.
func (d *Daemon) publishNew(ctx context.Context, live map[int]discover.Listener) []int {
	var fresh []int
	for port := range live {
		if e, ok := d.owned[port]; ok {
			e.missing = 0
			continue
		}
		if d.foreign[port] {
			continue
		}
		fresh = append(fresh, port)
	}
	if len(fresh) == 0 {
		return nil
	}
	sort.Ints(fresh)

	if d.DryRun {
		for _, port := range fresh {
			l := live[port]
			slog.Info("would publish", "port", port, "proc", l.Proc, "source", l.Source)
		}
		return nil
	}

	// A mapping that appeared since startup without us making it is the user's
	// too. Only worth a round trip when something new actually showed up.
	published, err := d.Pub.Published(ctx)
	if err != nil {
		slog.Warn("cannot read serve config", "err", err)
		return nil
	}
	keep := fresh[:0]
	for _, port := range fresh {
		if published[port] {
			d.foreign[port] = true
			slog.Info("leaving a mapping it did not create", "port", port)
			continue
		}
		keep = append(keep, port)
	}
	if fresh = keep; len(fresh) == 0 {
		return nil
	}

	if err := d.Pub.Publish(ctx, fresh); err != nil {
		// Nothing was written, so the whole batch is retried on the next poll.
		slog.Error("publish failed", "ports", fresh, "err", err)
		return nil
	}
	for _, port := range fresh {
		l := live[port]
		d.owned[port] = &entry{proc: l.Proc, source: l.Source}
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
	return fresh
}

// withdrawGone drops the ports we own that have been absent for the grace period.
func (d *Daemon) withdrawGone(ctx context.Context, live map[int]discover.Listener) {
	var expired []int
	for port, e := range d.owned {
		if _, ok := live[port]; ok {
			continue
		}
		e.missing++
		if e.missing < d.Cfg.Grace {
			continue
		}
		expired = append(expired, port)
	}
	if len(expired) == 0 {
		return
	}
	sort.Ints(expired)
	if err := d.Pub.Withdraw(ctx, expired); err != nil {
		slog.Error("withdraw failed", "ports", expired, "err", err)
		return
	}
	for _, port := range expired {
		e := d.owned[port]
		delete(d.owned, port)
		slog.Info("withdrew", "port", port)
		notify.All(ctx, d.Notifiers, notify.Event{
			Kind: "down", Port: port, Proc: e.proc, Source: e.source,
			Text: fmt.Sprintf("port %d is gone", port),
		})
	}
}

// shutdown withdraws every mapping this run created, so a stopped daemon does
// not leave the tailnet serving ports nobody is watching.
func (d *Daemon) shutdown() {
	if len(d.owned) == 0 {
		return
	}
	ports := make([]int, 0, len(d.owned))
	for port := range d.owned {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.Pub.Withdraw(ctx, ports); err != nil {
		slog.Warn("withdraw on shutdown failed", "ports", ports, "err", err)
	}
}

// listening reports every port with a local listener, before any policy is
// applied. reclaim needs the unfiltered view: a mapping for a port the config
// would not publish is by definition not one the daemon made.
func (d *Daemon) listening(ctx context.Context) (map[int]bool, error) {
	out := map[int]bool{}
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
			out[l.Port] = true
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
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
