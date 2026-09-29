# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Prebuilt binaries on every release: a universal binary for macOS and amd64
  and arm64 builds for Linux, with SHA-256 checksums. Installing no longer
  needs Go.
- A demo GIF in the README, and a comparison with `tailscale serve` and
  tsdproxy.

## [0.1.0] - 2026-09-20

First tagged release. The daemon watches for local dev servers and publishes
them on the tailnet, without per-project setup.

### Added

- Daemon that detects listening dev servers and publishes each one on the
  tailnet, withdrawing the mapping when the server stops.
- `service install`, `service uninstall` and `service status` subcommands; a
  single command sets the daemon up as a user service, on macOS too.
- Four publish policies, `dev`, `agent`, `both` and `all`, chosen at install
  time and overridable for a single run with `-mode`.
- Agent mode: a port is published when its process descends from a coding
  agent, including Antigravity (agy) and grok. Ports in the ephemeral range are
  ignored, so an agent's own RPC sockets never become a URL that dies seconds
  later.
- IPv6 listeners are found, including the interface zone macOS writes
  (`[::1%lo0]:5173`), which used to make an IPv6-only dev server invisible.
- Optional notifications, off by default, to Telegram or a webhook. The text of
  each event kind is a Go template, and `webhook.body` replaces the whole JSON
  body for receivers that expect another shape, such as Discord or ntfy.
- `env_file` in the configuration: a `KEY=value` file read at startup, so
  `token_env` can name a variable you already keep in one place.
- The config file is searched in XDG order first, so `~/.config` works the same
  on macOS and Linux.
- A version without `-ldflags`: `go install` reports the module version the
  toolchain recorded, or `dev+<commit>` from a checkout.
- Mappings made by hand are left alone. The daemon records what it published in
  a state file and withdraws nothing else, so a `tailscale serve` you set up
  yourself survives its startup and its shutdown.

[Unreleased]: https://github.com/breakzplatform/ts-autoserve/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/breakzplatform/ts-autoserve/releases/tag/v0.1.0
