# The README demo: a dev server starts and its tailnet URL appears, then it
# stops and the mapping is withdrawn.
#
# Honest-simulation note: this is recorded in a container with no tailnet.
# tailscaled is replaced by docs/demo/fake-tailscaled, which answers the local
# API calls and nothing else, so laptop.example-tailnet.ts.net is a made-up
# hostname that serves nothing. ts-autoserve is the real binary built from
# this repo, finding the Vite server through `ss` as it would on any Linux box.
# The only config change is `interval: 1s` (default 5s) to keep the GIF short.
#
# Record from the repo root:
#   docker build -f docs/demo/Dockerfile -t ts-autoserve-demo .
#   docker run --rm -v "$PWD/docs:/vhs" ts-autoserve-demo demo.tape

Output demo.gif

Set Shell bash
Set FontSize 16
Set Width 1400
Set Height 520
Set Padding 12
Set Theme "Catppuccin Mocha"
Set TypingSpeed 60ms
Set CursorBlink false

Hide
Type "fake-tailscaled 2>/dev/null &" Enter
Type "tmux new-session -s demo \; select-pane -T 'ts-autoserve   (simulated tailnet: laptop.example-tailnet.ts.net is not a real host)' \; split-window -v -l 65% -c /demo/app \; select-pane -T 'dev server' \; select-pane -t 0" Enter
Sleep 1s
Type "clear" Enter
Ctrl+B
Type "o"
Type "clear" Enter
Ctrl+B
Type "o"
Sleep 500ms
Show

Type "ts-autoserve" Sleep 400ms Enter
Sleep 2s

Ctrl+B
Type "o"
Type "npm run dev" Sleep 400ms Enter
Sleep 5s

Ctrl+C
Sleep 5s
