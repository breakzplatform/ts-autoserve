# ts-autoserve

Publish local dev servers on your tailnet, automatically.

Start a dev server on your laptop and your phone can open it at
`https://<machine>.<tailnet>.ts.net:<port>/`. You don't run a command, remember
a port, or configure each project. Stop the server and the mapping goes away.

It's meant for developing from a phone. If you turn notifications on, the URL
arrives on Telegram (or any webhook) as soon as the server is up. Notifications
are off by default, and the daemon works fine without ever sending a message.

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
mapping through tailscaled's local API. When the port goes away, it removes the
mapping.

Tailscale handles TLS and identity. Only devices in your tailnet can reach the
published ports, and only if your tailnet policy lets them.

### Which ports get published

Publishing everything that listens is a bad idea. A laptop runs plenty of local
infrastructure (editor bridges, sync daemons, debug ports) that has no business
on the network. Checking whether a port answers HTTP doesn't help either,
because those daemons answer HTTP too.

So a port is judged by two tests:

- **Is it a known dev-server port?** Covers 3000-3010, 5173-5183, 4200, 4321,
  8000-8010, 8080-8090, 6006, 8888, 19000-19006 and a few more.
- **Does its process descend from a coding agent?** Matches Claude Code,
  Codex, Cursor, Windsurf, Antigravity (`agy`), aider, opencode, goose, Devin,
  Copilot, Grok and anything else you add to the pattern.
  The test looks at process ancestry, so `npm run dev` started inside an agent
  counts on any port. Only the ancestors are matched, not the listening process
  itself, so a background daemon that happens to have an agent's name in its
  path doesn't count as a dev server.

The mode decides how the two tests combine. Set it with `mode:` in the config or
`-mode` on the command line:

| Mode | Publishes | Good for |
|---|---|---|
| `both` | a port that passes either test (the default) | a laptop you develop on |
| `dev` | only the known dev-server ports | predictability: nothing new is published unless you list the port |
| `agent` | only ports whose process descends from a coding agent | a machine where agents do the work and you want nothing else exposed |
| `all` | every port in `port_range` | short debugging sessions; noisy, so read [SECURITY.md](SECURITY.md) first |

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

See what it would do without changing anything:

```bash
ts-autoserve -once -dry-run
```

Then install it as a service (single command, no files to copy):

```bash
ts-autoserve service install     # launchd on macOS, systemd --user on Linux
ts-autoserve service status      # installed? running? which file, which config?
```

To pick a mode other than `both` at install time:

```bash
ts-autoserve service install -mode agent   # publish only what an agent started
```

That writes `mode:` into the config file, where you can see it and change it
later without reinstalling. The installer never rewrites an existing config. If
yours already sets a different mode, the installer tells you and leaves the
choice to you.

The installer writes a service file that points at the binary you ran, starts
the service, and keeps it running. It comes back at login and restarts if it
dies. On a headless Linux box that nobody logs into, add
`sudo loginctl enable-linger $USER` so the user service starts at boot.

The files under `packaging/` are the same definitions, if you'd rather install
them by hand.

<details>
<summary><b>Let a coding agent install it</b> (a prompt to paste; it installs in <code>agent</code> mode)</summary>

The agent you already have open can do the whole thing:

```text
Install ts-autoserve on this machine so the dev servers you start are reachable
from my phone over Tailscale:

1. go install github.com/breakzplatform/ts-autoserve/cmd/ts-autoserve@latest
   and make sure the resulting binary is on PATH.
2. Run `ts-autoserve -once -dry-run -v` and show me what it would publish.
   If it fails because this user cannot change the serve config, stop and ask
   me to run `sudo tailscale set --operator=$USER` -- that needs my password,
   do not try it yourself.
3. Run `ts-autoserve service install -mode agent`. That publishes only ports
   opened by processes you started, and nothing else on my machine.
4. Run `ts-autoserve service status` and tell me the URL pattern my ports will
   appear at.

Do not add a Telegram token or any other credential unless I give you one.
```

`-mode agent` makes sense here. When an agent runs the installer, the servers
worth publishing are the ones it starts, and a port you opened yourself stays
off the tailnet until you say otherwise. If you want your own dev servers
published too, change `mode:` in the config to `both`.

</details>

## Configure

Everything has a default, so the file is optional.

```yaml
mode: both          # dev | agent | both | all
interval: 5s        # how often to poll
grace: 2            # polls a port may be missing before it is withdrawn

dev_ports: ["3000-3010", "5173-5183", "8080-8090"]
exclude_ports: ["9222", "5000"]
port_range: ["3000-9999"]   # only used by mode "all"

agent_pattern: "claude|codex|cursor|windsurf|antigravity|\\bagy\\b|aider|opencode|goose|devin|copilot|\\bgrok\\b"

# Optional. KEY=value lines read into the environment at startup, so token_env
# can name a variable kept in a file you already have. Variables set in the
# real environment win. Values may be quoted; $VAR is expanded.
env_file: ~/.secrets/env

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

The daemon looks for the file in this order:

1. `$XDG_CONFIG_HOME/ts-autoserve/config.yaml`
2. `~/.config/ts-autoserve/config.yaml`
3. the platform config dir (`~/Library/Application Support/ts-autoserve/config.yaml` on macOS)

`-config` overrides the search.

The daemon also keeps a state file at `$XDG_STATE_HOME/ts-autoserve/state.json`
(or `~/.local/state/ts-autoserve/state.json`) that records which mappings it
created. Don't edit it. If you delete it, the daemon forgets which mappings were
its own and won't clean up any of them.

The webhook receives one JSON object per event:

```json
{"kind":"up","port":5173,"url":"https://laptop.example-tailnet.ts.net:5173/","proc":"node","source":"host","text":"node up on port 5173\nhttps://..."}
```

### Tokens and the service

A service doesn't inherit your shell's environment, so a token named by
`token_env` has to reach it another way:

- **Copied at install:** export the variable before running `service install`.
  The installer copies its current value into the service definition, which is
  mode 600.
- **Read from a file:** point `env_file:` at a `KEY=value` file you already keep.
  The daemon reads it at startup and nothing is copied, so rotating a token means
  editing that file and restarting the service.

### Changing the messages

Each kind of event has a [Go template](https://pkg.go.dev/text/template) for
its text. That text is what Telegram shows and what the webhook sends as `text`.
Set a template to replace the built-in one, or set it to `""` to stop sending
that kind of event.

```yaml
notify:
  messages:
    up: "🟢 {{.Label}} → {{.URL}}"         # built-in: "{{.Label}} up on port {{.Port}}\n{{.URL}}"
    down: ""                                # muted
    start: "back up, serving {{join .Ports}}"
```

A template sees `.Kind` (`up`, `down`, `start`), `.Port`, `.URL`, `.Proc`,
`.Source`, `.Ports` (on `start`, every port already served) and `.Label` (the
process name, or "a local server"). `join` lists ports as `3000, 5173`.

If the receiving end expects a different JSON shape, `webhook.body` replaces the
whole body. It sees the same fields plus `.Text`, and `json` quotes a value
safely:

```yaml
notify:
  webhook:
    enabled: true
    url: https://discord.com/api/webhooks/...
    body: '{"content": {{json .Text}}}'
    content_type: application/json   # the default
```

## Commands and flags

```
ts-autoserve [flags]              run the daemon
ts-autoserve service install      install and start it as a user service
ts-autoserve service uninstall    stop the user service and remove it
ts-autoserve service status       is the service installed and running?
ts-autoserve version              print version
```

With no command, it runs the daemon in the foreground, which is handy for
watching it work before you hand it to the OS.

| Flag | Meaning |
|---|---|
| `-config PATH` | config file (default: first path that exists, see above) |
| `-mode MODE` | `dev`, `agent`, `both` or `all`; overrides the config for this run, and on `service install` is written to the config |
| `-once` | single pass, then exit |
| `-dry-run` | report what would change, change nothing |
| `-v` | debug logging |

## What it will not do

- **Touch mappings it didn't create.** A `tailscale serve` you set up by hand is
  left alone: the daemon never publishes over it or withdraws it, whether or not
  its server is running. The serve config doesn't record who made a mapping, so
  the daemon relies on its own state file. Anything it didn't write down as its
  own is yours, including a mapping you make while it's running.
- **Expose anything publicly.** It only uses Serve (tailnet-only), never Funnel.
- **Leave its own mappings behind.** On shutdown it withdraws what it published.
  On startup it clears what a previous run left behind, but only mappings that run
  recorded as its own, and only where the port has stopped listening.

## Roadmap

- **v0.1:** host ports, macOS and Linux, Telegram and webhook notifications.
- **v0.2:** Docker containers with published ports, discovered through the Docker
  API and mapped the same way. That means one host with one port per service, not
  one tailnet device per container.
- **v0.3:** opt-in Funnel per port, and Tailscale Services so a service can get
  its own MagicDNS name instead of sharing the node's.

## Security

Publishing ports automatically has risks. Read [SECURITY.md](SECURITY.md) before
running it on a machine that has more than dev servers on it.

## License

MIT. See [LICENSE](LICENSE).
