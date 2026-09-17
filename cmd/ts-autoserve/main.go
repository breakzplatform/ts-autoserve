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
	"strings"
	"syscall"

	"github.com/breakzplatform/ts-autoserve/internal/config"
	"github.com/breakzplatform/ts-autoserve/internal/daemon"
	"github.com/breakzplatform/ts-autoserve/internal/discover"
	"github.com/breakzplatform/ts-autoserve/internal/notify"
	"github.com/breakzplatform/ts-autoserve/internal/service"
	"github.com/breakzplatform/ts-autoserve/internal/state"
	"github.com/breakzplatform/ts-autoserve/internal/tsserve"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	// Commands come first, flags after. Everything about the OS service is
	// grouped under "service", because that is a different subject from the
	// daemon's own work: "service status" is whether the daemon is running,
	// while a later plain "status" will be what it has published.
	//   ts-autoserve                      run the daemon
	//   ts-autoserve service install      install and start the user service
	//   ts-autoserve service uninstall    stop and remove it
	//   ts-autoserve service status       is it installed and running?
	//   ts-autoserve version
	cmd, sub := "", ""
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet("ts-autoserve", flag.ExitOnError)
	fs.Usage = usage(fs)
	var (
		cfgPath = fs.String("config", config.DefaultPath(), "path to config.yaml")
		once    = fs.Bool("once", false, "run a single pass and exit")
		dryRun  = fs.Bool("dry-run", false, "report what would be published, change nothing")
		verbose = fs.Bool("v", false, "debug logging")
	)
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	var err error
	switch cmd {
	case "":
		err = run(*cfgPath, *once, *dryRun)
	case "service":
		err = serviceCommand(sub, fs)
	case "version":
		fmt.Println("ts-autoserve", version)
	case "help":
		fs.Usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		fs.Usage()
		os.Exit(2)
	}
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func usage(fs *flag.FlagSet) func() {
	return func() {
		fmt.Fprint(os.Stderr, `ts-autoserve publishes local dev servers on your tailnet.

Usage:
  ts-autoserve [flags]              run the daemon
  ts-autoserve service install      install and start it as a user service
  ts-autoserve service uninstall    stop the user service and remove it
  ts-autoserve service status       is the service installed and running?
  ts-autoserve version              print version

Flags:
`)
		fs.PrintDefaults()
	}
}

// serviceCommand handles everything about running as an OS service.
func serviceCommand(sub string, fs *flag.FlagSet) error {
	switch sub {
	case "install":
		return manageService(true)
	case "uninstall":
		return manageService(false)
	case "status":
		st, err := service.Status()
		if err != nil {
			return err
		}
		state := "not installed"
		switch {
		case st.Installed && st.Running:
			state = "running"
		case st.Installed:
			state = "installed, not running"
		}
		fmt.Printf("service: %s (%s)\n", state, st.Detail)
		fmt.Println("file:", st.Path)
		fmt.Println("config:", config.DefaultPath())
		return nil
	case "":
		fs.Usage()
		return fmt.Errorf("service needs a subcommand: install, uninstall or status")
	default:
		fs.Usage()
		return fmt.Errorf("unknown service subcommand %q", sub)
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
	d.Store = state.New(state.DefaultPath())

	slog.Info("ts-autoserve starting",
		"version", version, "node", host, "mode", cfg.Mode,
		"interval", cfg.Interval, "grace", cfg.Grace, "dry_run", dryRun,
		"state", state.DefaultPath())

	if once {
		return d.Poll(ctx)
	}
	return d.Run(ctx)
}
