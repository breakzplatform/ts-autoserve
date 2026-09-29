# Image for recording docs/demo.gif. Build and record from the repo root:
#
#   docker build -f docs/demo/Dockerfile -t ts-autoserve-demo .
#   docker run --rm -v "$PWD/docs:/vhs" ts-autoserve-demo demo.tape
#
# ts-autoserve is built from this checkout and runs unmodified. Only tailscaled
# is simulated (see fake-tailscaled), because a container has no tailnet.

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=demo" -o /out/ts-autoserve ./cmd/ts-autoserve \
 && CGO_ENABLED=0 go build -trimpath -o /out/fake-tailscaled ./docs/demo/fake-tailscaled

FROM ghcr.io/charmbracelet/vhs:v0.12.1
# Without a UTF-8 locale tmux draws non-ASCII (Vite's arrows) as "_".
ENV LANG=C.UTF-8
RUN apt-get update \
 && apt-get install -y --no-install-recommends iproute2 tmux nodejs npm \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /demo/app
RUN npm init -y >/dev/null \
 && npm install --no-audit --no-fund --loglevel=error vite@7 \
 && npm pkg set name=app scripts.dev="vite --port 5173 --clearScreen false" \
 && echo '<h1>hello from the laptop</h1>' > index.html
COPY docs/demo/tmux.conf /root/.tmux.conf
RUN echo "PS1='\$ '" >> /root/.bashrc
# Poll every second instead of every five, so the recording stays short.
RUN mkdir -p /root/.config/ts-autoserve \
 && printf 'interval: 1s\n' > /root/.config/ts-autoserve/config.yaml
COPY --from=build /out/ /usr/local/bin/
WORKDIR /vhs
