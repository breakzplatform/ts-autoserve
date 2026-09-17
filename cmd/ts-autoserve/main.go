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
	"path/filepath"
	"syscall"

	"github.com/breakzplatform/ts-autoserve/internal/config"
	"github.com/breakzplatform/ts-autoserve/internal/daemon"
	"github.com/breakzplatform/ts-autoserve/internal/discover"
	"github.com/breakzplatform/ts-autoserve/internal/notify"
	"github.com/breakzplatform/ts-autoserve/internal/service"
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
		install     = flag.Bool("install", false, "install as a user service (launchd or systemd) and start it")
		uninstall   = flag.Bool("uninstall", false, "stop the user service and remove it")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("ts-autoserve", version)
		return
	}

	if *install || *uninstall {
		if err := manageService(*install); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
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

// manageService installs or removes the OS service, so nobody has to copy a
// plist or a unit file by hand.
func manageService(install bool) error {
	if !install {
		path, err := service.Uninstall()
		if err != nil {
			return err
		}
		fmt.Println("removed", path)
		return nil
	}

	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find my own path: %w", err)
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return err
	}

	// Carry the Telegram token through, if the config asks for one and it is
	// set here: a service does not inherit the shell's environment.
	env := map[string]string{}
	if cfg, err := config.Load(config.DefaultPath()); err == nil {
		if name := cfg.Notify.Telegram.TokenEnv; name != "" {
			if v := os.Getenv(name); v != "" {
				env[name] = v
			}
		}
	}

	path, err := service.Install(bin, env)
	if err != nil {
		return err
	}
	fmt.Println("installed and started:", path)
	fmt.Println("running:", bin)
	fmt.Println("config:", config.DefaultPath())
	if hint := service.LingerHint(); hint != "" {
		fmt.Println("note:", hint)
	}
	return nil
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
