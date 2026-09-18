// Package daemon polls the discovery sources and keeps the serve config in sync.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
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

// Store remembers the ports this daemon published, so a later run can tell
// them from the mappings the user made. A nil Store means no memory, and the
// daemon then withdraws nothing it finds at startup.
type Store interface {
	Load() (map[int]bool, error)
	Save(ports []int) error
}

// Daemon watches local ports and mirrors them onto the tailnet.
type Daemon struct {
	Cfg       config.Config
	Sources   []discover.Source
	Pub       Publisher
	Notifiers []notify.Notifier
	Messages  *notify.Messages // nil: the built-in text
	Store     Store
	DryRun    bool

	spawned  func(pid int) bool // the ancestry walk; agent.Detector.Spawned
	ancestry map[process]bool   // spawned's verdict, for processes still listening
	owned    map[int]*entry     // ports this daemon published, by port
	foreign  map[int]bool       // published by someone else: never ours to touch
	started  bool
}

// process names a listening process. The name guards against a pid reused
// between two polls inheriting the previous process's verdict.
type process struct {
	pid  int
	proc string
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
		spawned:   agent.Detector{Pattern: cfg.Agent}.Spawned,
		ancestry:  map[process]bool{},
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

// reclaim sorts out what is already in the serve config at startup.
//
// Only the ports the last run wrote down are the daemon's to clean up. One of
// those that is still listening is re-adopted; one whose server is gone is
// litter from a run that died, and goes. Everything else in the config belongs
// to the user -- a `tailscale serve` is persistent, and the server behind it is
// often stopped at the moment this daemon starts, so "nothing is listening on
// it" says nothing about who put it there. Those are remembered as foreign:
// never published over, never withdrawn.
func (d *Daemon) reclaim(ctx context.Context) error {
	published, err := d.Pub.Published(ctx)
	if err != nil {
		return err
	}
	mine, err := d.remembered()
	if err != nil {
		// The state file is the only record of what is ours. Without it,
		// withdrawing nothing is the safe way to be wrong.
		slog.Warn("cannot read state; leaving every existing mapping alone", "err", err)
		mine = nil
	}
	listening, err := d.listening(ctx)
	if err != nil {
		return err
	}

	var stale []int
	for port := range published {
		if !mine[port] {
			d.foreign[port] = true
			continue
		}
		// Ours. Keep it only if it still has a server and the config still
		// wants that port published: an exclusion added since the last run
		// takes effect now rather than whenever the server happens to stop.
		if l, ok := listening[port]; ok && d.wanted(l) {
			d.owned[port] = &entry{proc: l.Proc, source: l.Source}
			slog.Info("re-adopted", "port", port, "proc", l.Proc)
			continue
		}
		stale = append(stale, port)
	}
	sort.Ints(stale)
	if len(stale) > 0 {
		if d.DryRun {
			slog.Info("would withdraw stale mappings", "ports", stale)
			return nil
		}
		if err := d.Pub.Withdraw(ctx, stale); err != nil {
			slog.Warn("withdraw failed", "ports", stale, "err", err)
		} else {
			slog.Info("withdrew stale mappings", "ports", stale)
		}
	}
	d.save()
	return nil
}

func (d *Daemon) remembered() (map[int]bool, error) {
	if d.Store == nil {
		return nil, nil
	}
	return d.Store.Load()
}

// save records what the daemon currently has published, so a run that is killed
// rather than stopped still leaves the next one able to clean up after it.
func (d *Daemon) save() {
	if d.Store == nil || d.DryRun {
		return
	}
	ports := make([]int, 0, len(d.owned))
	for port := range d.owned {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	if err := d.Store.Save(ports); err != nil {
		slog.Warn("cannot write state", "err", err)
	}
}

// Poll runs one reconciliation pass.
func (d *Daemon) Poll(ctx context.Context) error {
	live, err := d.candidates(ctx)
	if err != nil {
		return err
	}

	fresh := d.publishNew(ctx, live)
	gone := d.withdrawGone(ctx, live)
	if len(fresh) > 0 || gone > 0 {
		d.save()
	}

	if !d.started {
		d.started = true
		if len(fresh) > 0 {
			d.notify(ctx, notify.Event{Kind: "start", Ports: fresh})
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
			d.notify(ctx, notify.Event{
				Kind: "up", Port: port, URL: url, Proc: l.Proc, Source: l.Source,
			})
		}
	}
	return fresh
}

// withdrawGone drops the ports we own that have been absent for the grace
// period, and reports how many went.
func (d *Daemon) withdrawGone(ctx context.Context, live map[int]discover.Listener) int {
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
		return 0
	}
	sort.Ints(expired)
	if err := d.Pub.Withdraw(ctx, expired); err != nil {
		slog.Error("withdraw failed", "ports", expired, "err", err)
		return 0
	}
	for _, port := range expired {
		e := d.owned[port]
		delete(d.owned, port)
		slog.Info("withdrew", "port", port)
		d.notify(ctx, notify.Event{
			Kind: "down", Port: port, Proc: e.proc, Source: e.source,
		})
	}
	return len(expired)
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
		// Leave the state file alone: the next run inherits the cleanup.
		slog.Warn("withdraw on shutdown failed", "ports", ports, "err", err)
		return
	}
	d.owned = map[int]*entry{}
	d.save()
}

// listening reports every local listener, before any policy is applied.
// reclaim needs the unfiltered view, because a port the config would not
// publish today may still be one this daemon published yesterday.
func (d *Daemon) listening(ctx context.Context) (map[int]discover.Listener, error) {
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

// candidates asks every source what is listening and keeps what the policy wants.
func (d *Daemon) candidates(ctx context.Context) (map[int]discover.Listener, error) {
	all, err := d.listening(ctx)
	if err != nil {
		return nil, err
	}
	d.forgetAncestry(all)
	out := make(map[int]discover.Listener, len(all))
	for port, l := range all {
		if d.wanted(l) {
			out[port] = l
		}
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
	key := process{l.PID, l.Proc}
	if v, ok := d.ancestry[key]; ok {
		return v
	}
	v := d.spawned(l.PID)
	d.ancestry[key] = v
	return v
}

// forgetAncestry drops the verdicts of processes that no longer listen.
//
// Walking the ancestry costs one ps per parent on macOS, and the same ports are
// judged every poll; a process's ancestry does not change while it lives, so
// each one is walked once, when it first appears.
func (d *Daemon) forgetAncestry(live map[int]discover.Listener) {
	keep := make(map[process]bool, len(live))
	for _, l := range live {
		keep[process{l.PID, l.Proc}] = true
	}
	for key := range d.ancestry {
		if !keep[key] {
			delete(d.ancestry, key)
		}
	}
}

// notify renders an event's text and sends it, unless the config muted it.
func (d *Daemon) notify(ctx context.Context, ev notify.Event) {
	if len(d.Notifiers) == 0 || !d.Messages.Render(&ev) {
		return
	}
	notify.All(ctx, d.Notifiers, ev)
}
