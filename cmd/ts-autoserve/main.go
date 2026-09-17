// Command ts-autoserve publishes local dev servers on your tailnet.
//
// It watches the ports that come and go on this machine and keeps a matching
// `tailscale serve` mapping for each one, so a server started on a laptop is
// reachable from a phone at https://<machine>.<tailnet>.ts.net:<port>/ without
// anyone running a command.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/breakzplatform/ts-autoserve/internal/config"
	"github.com/breakzplatform/ts-autoserve/internal/daemon"
	"github.com/breakzplatform/ts-autoserve/internal/discover"
	"github.com/breakzplatform/ts-autoserve/internal/notify"
	"github.com/breakzplatform/ts-autoserve/internal/tsserve"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	var (
		cfgPath     = flag.String("config", config.DefaultPath(), "path to config.yaml")
		once        = flag.Bool("once", false, "run a single pass and exit")
		dryRun      = flag.Bool("dry-run", false, "report what would be published, change nothing")
		showVersion = flag.Bool("version", false, "print version and exit")
		verbose     = flag.Bool("v", false, "debug logging")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("ts-autoserve", version)
		return
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if err := run(*cfgPath, *once, *dryRun); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func run(cfgPath string, once, dryRun bool) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	pub := tsserve.New()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	host, err := pub.DNSName(ctx)
	if err != nil {
		return fmt.Errorf("%w (is tailscaled running, and does this user have serve permission? see README)", err)
	}

	d := daemon.New(cfg, []discover.Source{discover.Host{}}, pub, notify.FromConfig(cfg.Notify))
	d.DryRun = dryRun

	slog.Info("ts-autoserve starting",
		"version", version, "node", host, "mode", cfg.Mode,
		"interval", cfg.Interval, "grace", cfg.Grace, "dry_run", dryRun)

	if once {
		return d.Poll(ctx)
	}
	return d.Run(ctx)
}
