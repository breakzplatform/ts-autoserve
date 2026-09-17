# ts-autoserve

Publish local dev servers on your tailnet, automatically.

Start a dev server on your laptop and it becomes reachable from your phone at
`https://<machine>.<tailnet>.ts.net:<port>/` — no command to run, no port to
remember, no config per project. Stop the server and the mapping goes away.

Built for developing from a phone: if you turn notifications on, the URL arrives
on Telegram (or any webhook) the moment the server is up. Notifications are off
by default — the daemon works fine without ever sending a message anywhere.

```
$ npm run dev
  VITE ready in 412 ms  ➜  http://localhost:5173/

# on your phone, a second later:
  node up on port 5173
  https://laptop.example-tailnet.ts.net:5173/
```

## How it works

A small daemon polls the machine's listening TCP sockets (`lsof` on macOS,
`ss` on Linux). When a port it cares about appears, it writes a `tailscale serve`
mapping through tailscaled's local API; when the port goes away, it removes it.

Tailscale handles TLS and identity. Only devices in your tailnet can reach the
published ports, and only if your tailnet policy lets them.

### Which ports get published

Publishing *everything* that listens is a bad idea — a laptop is full of local
infrastructure (editor bridges, sync daemons, debug ports) that has no business
on the network. Filtering by "does it answer HTTP?" does not help either: those
daemons answer HTTP too.

So the default policy (`mode: both`) publishes a port when **either** is true:

- **It is a known dev-server port** — 3000-3010, 5173-5183, 4200, 4321,
  8000-8010, 8080-8090, 6006, 8888, 19000-19006 and friends.
- **Its process descends from a coding agent** — Claude Code, Codex, Cursor,
  Antigravity (`agy`), aider, opencode, goose and anything else you add to the
  pattern.
  The test is process ancestry, so `npm run dev` started inside an agent counts,
  whatever port it picked. The listening process itself is not matched, only its
  ancestors: a background daemon that merely carries an agent's name in its path
  is not a dev server.

Other modes: `dev` (allowlist only), `agent` (agent-spawned only), `all`
(everything in `port_range` — noisy, read [SECURITY.md](SECURITY.md) first).

`exclude_ports` always wins, whatever the mode. The defaults exclude Chrome's
remote-debugging port and a few others that would hand over more than a preview.

## Install

Requires Go 1.24+ and a working Tailscale install.

```bash
go install github.com/breakzplatform/ts-autoserve/cmd/ts-autoserve@latest
```

Grant your user permission to change the serve config (once per machine):

```bash
sudo tailscale set --operator=$USER
```

Check what it would do, without changing anything:

```bash
ts-autoserve -once -dry-run
```

Then install it as a service — one command, no file to copy:

```bash
ts-autoserve service install     # launchd on macOS, systemd --user on Linux
ts-autoserve service status      # installed? running? which file, which config?
ts-autoserve service uninstall   # stop it and remove the service file
```

It writes the service file pointing at the binary you ran, starts it, and keeps
it running: **it comes back at login and restarts if it dies.** On a headless
Linux box that nobody logs into, add `sudo loginctl enable-linger $USER` so the
user service starts at boot.

If a notification token is configured through `token_env`, `service install` copies its
current value into the service definition, which is written mode 600 — a service
does not inherit your shell's environment.

The files under `packaging/` are the same definitions, for anyone who would
rather install them by hand.

## Configure

Everything has a default; the file is optional. It is looked for in this order,
on every platform including macOS:

1. `$XDG_CONFIG_HOME/ts-autoserve/config.yaml`
2. `~/.config/ts-autoserve/config.yaml`
3. the platform config dir (`~/Library/Application Support/ts-autoserve/config.yaml` on macOS)

or wherever `-config` points.

```yaml
mode: both          # dev | agent | both | all
interval: 5s        # how often to poll
grace: 2            # polls a port may be missing before it is withdrawn

dev_ports: ["3000-3010", "5173-5183", "8080-8090"]
exclude_ports: ["9222", "5000"]
port_range: ["3000-9999"]   # only used by mode "all"

agent_pattern: "claude|codex|cursor|antigravity|\\bagy\\b|aider|opencode"

# Optional. With this block absent, nothing is ever sent anywhere.
notify:
  telegram:
    enabled: true
    token_env: TELEGRAM_BOT_TOKEN   # keep the token out of the file
    chat_id: "123456789"
  webhook:
    enabled: false
    url: https://example.com/hook
```

The webhook receives one JSON object per event:

```json
{"kind":"up","port":5173,"url":"https://laptop.example-tailnet.ts.net:5173/","proc":"node","source":"host","text":"node up on port 5173\nhttps://..."}
```

## Commands and flags

```
ts-autoserve [flags]              run the daemon
ts-autoserve service install      install and start it as a user service
ts-autoserve service uninstall    stop the user service and remove it
ts-autoserve service status       is the service installed and running?
ts-autoserve version              print version
```

Running it with no command runs the daemon in the foreground — useful to watch
it work before handing it to the OS.

| Flag | Meaning |
|---|---|
| `-config PATH` | config file (default: first path that exists, see above) |
| `-once` | single pass, then exit |
| `-dry-run` | report what would change, change nothing |
| `-v` | debug logging |

## What it will not do

- **Touch mappings it did not create.** A `tailscale serve` you set up by hand
  for a port that is still listening is left alone.
- **Expose anything publicly.** It only uses Serve (tailnet-only), never Funnel.
- **Outlive itself.** On shutdown it withdraws what it published; on startup it
  clears mappings left behind by a previous run whose ports are gone.

## Roadmap

- **v0.1** — host ports, macOS and Linux, Telegram and webhook notifications.
- **v0.2** — Docker source: containers with published ports, discovered through
  the Docker API, mapped the same way (one host, one port per service — not one
  tailnet device per container).
- **v0.3** — opt-in Funnel per port, and Tailscale Services so a service can get
  its own MagicDNS name instead of sharing the node's.

## Security

Automatically publishing ports is a real trade-off. Read
[SECURITY.md](SECURITY.md) before running it on a machine that has more than dev
servers on it.

## License

MIT — see [LICENSE](LICENSE).
