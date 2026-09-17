# Security

ts-autoserve makes local services reachable from other machines without anyone
asking for it each time. That is the feature, and it is also the whole risk. This
document is the honest list.

## Threat model

Published ports are reachable by **every device and user in your tailnet**, and
by anyone those users have shared a device with. Tailscale terminates TLS and
applies your policy file; it does not add authentication to an app that has none.

The daemon never uses Funnel, so nothing is exposed to the public internet unless
you configure Funnel yourself.

## The main risks

### 1. Publishing something that is not a dev server

A laptop listens on more than dev servers. Some ports are catastrophic to share:

| Port | What it is | What sharing it means |
|---|---|---|
| 9222 | Chrome remote debugging | Full control of the browser: read cookies, sessions, any logged-in account. Excluded by default. |
| 4444 | WebDriver / Selenium | Drive the browser, same outcome. |
| 5900 | VNC | The desktop. |
| 6379, 27017, 5432, 3306 | Redis, Mongo, Postgres, MySQL | Usually no password on localhost. |
| 2375, 2376 | Docker API | Root on the host, in practice. |
| 8888 | Jupyter | Arbitrary code execution, if the token is weak or absent. |

This is why the default is an allowlist plus agent ancestry, not "every port".
`mode: all` removes that protection — use it only on a machine where you know
everything that listens, and keep `exclude_ports` current.

### 2. Dev servers assume localhost is private

A dev server is not a hardened server. It typically has no authentication, wide
open CORS, source maps, verbose stack traces, an HMR websocket that accepts any
origin, and `.env` values inlined into the bundle. Several framework dev servers
have shipped arbitrary-file-read bugs, on the theory that only the developer can
reach them. Publishing one on the tailnet moves it into reach of every device you
own — including any that is lost, shared, or compromised.

### 3. Tailnet reach is wider than people remember

"Tailnet only" means every device you own, every user in the tailnet, every
device shared into it, and any tagged CI node. If that is broader than you want
for a dev server, restrict it in the policy file with a grant that limits who can
reach this node's ports, rather than relying on the daemon.

### 4. Identity headers

Tailscale Serve injects `Tailscale-User-Login`, `Tailscale-User-Name` and
`Tailscale-User-Profile-Pic` into proxied requests. Two consequences: your app
learns who is calling (fine), and an app that *trusts* those headers must be sure
they cannot be spoofed by a request that did not come through Serve. If the same
app also listens on localhost directly, anything local can set those headers.

### 5. Agent mode broadens the surface

`mode: agent` and `mode: both` publish whatever an agent-spawned process is
listening on, including ports you never chose. An agent that starts an inspector,
a debug bridge, or a database container gets that port published too. Ancestry
matching is a heuristic: it skips the listening process itself to avoid matching
daemons named after agents, but it cannot tell a dev server from anything else an
agent happens to open. `exclude_ports` is the backstop.

### 6. It rewrites the serve config

The daemon needs operator permission (`tailscale set --operator=$USER`), which
lets it read and write the node's entire serve configuration. It performs a
read-modify-write per change, so a `tailscale serve` command run by hand at the
same moment can lose. Run it as your own user, never as root.

### 7. Notifications leak metadata

Every published URL, port and process name is sent to whatever notifier you
configure. That reveals what you are working on to Telegram, or to whoever
operates your webhook endpoint. Keep the Telegram token in an environment
variable (`token_env`) rather than the config file, and keep the config file
readable only by you (`chmod 600`).

## Hardening checklist

- Keep `mode` at `dev` or `both`; avoid `all`.
- Review `exclude_ports` for your machine — add database, debugger and admin
  ports before the first run, and check `-once -dry-run` output.
- Restrict who can reach the node's ports in your tailnet policy file.
- Do not run it on a machine that holds production credentials in a service
  that happens to listen on a dev port.
- Prefer `token_env` for the Telegram token; `chmod 600` the config.
- Do not add Funnel to a published port without understanding that it makes the
  service public to the internet, unauthenticated.

## Reporting a vulnerability

Open a private security advisory on the repository, or email the maintainer.
Please do not open a public issue for a vulnerability.
