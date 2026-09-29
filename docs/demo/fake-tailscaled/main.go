// Command fake-tailscaled stands in for tailscaled when recording the demo.
//
// It answers the three LocalAPI calls ts-autoserve makes (status, and reading
// and writing the serve config) on tailscaled's default Linux socket, so the
// real daemon can run unchanged in a container that has no tailnet. Nothing is
// served: the node name it reports, laptop.example-tailnet.ts.net, is made up.
package main

import (
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

const socket = "/var/run/tailscale/tailscaled.sock"

func main() {
	var (
		mu    sync.Mutex
		serve = []byte("{}")
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/localapi/v0/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"BackendState":"Running","Self":{"DNSName":"laptop.example-tailnet.ts.net."}}`)
	})
	mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.Write(serve)
		case http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			serve = body
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		log.Fatal(err)
	}
	os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(http.Serve(ln, mux))
}
