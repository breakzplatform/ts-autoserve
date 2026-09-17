// Package tsserve publishes local ports on the tailnet.
//
// It talks to tailscaled over the local API instead of shelling out to the
// tailscale binary: one round trip per change, and the serve config is read
// back as a struct, so the daemon can tell its own mappings from the ones a
// human set up by hand.
package tsserve

import (
	"context"
	"fmt"
	"strings"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
)

// Client publishes and withdraws HTTPS proxies for local ports.
type Client struct {
	lc     local.Client
	dnsSuf string // cached MagicDNS name, e.g. "laptop.example-tailnet.ts.net"
}

// New returns a client bound to the local tailscaled.
func New() *Client { return &Client{} }

// DNSName returns this node's MagicDNS name without the trailing dot.
func (c *Client) DNSName(ctx context.Context) (string, error) {
	if c.dnsSuf != "" {
		return c.dnsSuf, nil
	}
	st, err := c.lc.Status(ctx)
	if err != nil {
		return "", fmt.Errorf("tailscale status: %w", err)
	}
	if st.Self == nil || st.Self.DNSName == "" {
		return "", fmt.Errorf("this node has no MagicDNS name yet")
	}
	c.dnsSuf = strings.TrimSuffix(st.Self.DNSName, ".")
	return c.dnsSuf, nil
}

// URL is where a published port can be reached from the tailnet.
func (c *Client) URL(ctx context.Context, port int) (string, error) {
	host, err := c.DNSName(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s:%d/", host, port), nil
}

// Publish serves https://<node>:<port>/ as a proxy to 127.0.0.1:<port>.
//
// Every port goes in on one read-modify-write: a service that opens five ports
// at once is one round trip to tailscaled and one node update on the tailnet,
// not five, and nothing the user changes in between can be clobbered halfway.
func (c *Client) Publish(ctx context.Context, ports []int) error {
	return c.mutate(ctx, ports, func(sc *ipn.ServeConfig, hp ipn.HostPort, port int) {
		if sc.TCP == nil {
			sc.TCP = map[uint16]*ipn.TCPPortHandler{}
		}
		sc.TCP[uint16(port)] = &ipn.TCPPortHandler{HTTPS: true}
		if sc.Web == nil {
			sc.Web = map[ipn.HostPort]*ipn.WebServerConfig{}
		}
		sc.Web[hp] = &ipn.WebServerConfig{
			Handlers: map[string]*ipn.HTTPHandler{
				"/": {Proxy: fmt.Sprintf("http://127.0.0.1:%d", port)},
			},
		}
	})
}

// Withdraw removes the mappings for these ports, leaving the rest of the
// config alone.
func (c *Client) Withdraw(ctx context.Context, ports []int) error {
	return c.mutate(ctx, ports, func(sc *ipn.ServeConfig, hp ipn.HostPort, port int) {
		delete(sc.TCP, uint16(port))
		delete(sc.Web, hp)
		delete(sc.AllowFunnel, hp)
	})
}

// Published reports the ports this node currently serves over HTTPS.
func (c *Client) Published(ctx context.Context) (map[int]bool, error) {
	sc, err := c.lc.GetServeConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("get serve config: %w", err)
	}
	out := map[int]bool{}
	if sc == nil {
		return out, nil
	}
	for port, h := range sc.TCP {
		if h != nil && h.HTTPS {
			out[int(port)] = true
		}
	}
	return out, nil
}

func (c *Client) mutate(ctx context.Context, ports []int, fn func(*ipn.ServeConfig, ipn.HostPort, int)) error {
	if len(ports) == 0 {
		return nil
	}
	host, err := c.DNSName(ctx)
	if err != nil {
		return err
	}
	sc, err := c.lc.GetServeConfig(ctx)
	if err != nil {
		return fmt.Errorf("get serve config: %w", err)
	}
	if sc == nil {
		sc = &ipn.ServeConfig{}
	}
	for _, port := range ports {
		fn(sc, ipn.HostPort(fmt.Sprintf("%s:%d", host, port)), port)
	}
	// Hand back nil rather than an empty map, so withdrawing the last port
	// leaves the serve config as clean as it was before the daemon ran.
	if len(sc.TCP) == 0 {
		sc.TCP = nil
	}
	if len(sc.Web) == 0 {
		sc.Web = nil
	}
	if len(sc.AllowFunnel) == 0 {
		sc.AllowFunnel = nil
	}
	if err := c.lc.SetServeConfig(ctx, sc); err != nil {
		return fmt.Errorf("set serve config: %w", err)
	}
	return nil
}
