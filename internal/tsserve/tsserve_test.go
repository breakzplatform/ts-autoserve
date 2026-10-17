package tsserve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/breakzplatform/ts-autoserve/internal/status"
	"tailscale.com/ipn"
)

const node = "laptop.example-tailnet.ts.net"

// fakeAPI stands in for tailscaled's local API: the status call and reading
// and writing the serve config, with the same ETag check the real one makes,
// so a write based on a stale read is refused.
type fakeAPI struct {
	mu       sync.Mutex
	dnsName  string // as tailscaled reports it, with the trailing dot
	serve    []byte // the serve config JSON, as last written
	deny     bool   // answer every serve-config call with 403
	statuses int    // status calls served
	gets     int    // serve-config reads served
	sets     int    // serve-config writes accepted
}

func etag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/localapi/v0/status":
		f.statuses++
		self := map[string]any{}
		if f.dnsName != "" {
			self["DNSName"] = f.dnsName
		}
		json.NewEncoder(w).Encode(map[string]any{"BackendState": "Running", "Self": self})
	case "/localapi/v0/serve-config":
		if f.deny {
			http.Error(w, "serve config denied", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			f.gets++
			w.Header().Set("Etag", etag(f.serve))
			w.Write(f.serve)
		case http.MethodPost:
			if r.Header.Get("If-Match") != etag(f.serve) {
				http.Error(w, "etag mismatch", http.StatusPreconditionFailed)
				return
			}
			body, _ := io.ReadAll(r.Body)
			f.serve = body
			f.sets++
		}
	default:
		http.NotFound(w, r)
	}
}

// config decodes what the client last wrote.
func (f *fakeAPI) config(t *testing.T) *ipn.ServeConfig {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var sc *ipn.ServeConfig
	if err := json.Unmarshal(f.serve, &sc); err != nil {
		t.Fatalf("decode serve config %q: %v", f.serve, err)
	}
	if sc == nil {
		sc = &ipn.ServeConfig{}
	}
	return sc
}

func (f *fakeAPI) set(t *testing.T, sc *ipn.ServeConfig) {
	t.Helper()
	b, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.serve = b
	f.mu.Unlock()
}

// newClient returns a Client wired to a fake tailscaled.
func newClient(t *testing.T) (*Client, *fakeAPI) {
	t.Helper()
	f := &fakeAPI{dnsName: node + ".", serve: []byte("null")}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New()
	c.lc.OmitAuth = true
	c.lc.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}
	return c, f
}

func hp(port string) ipn.HostPort { return ipn.HostPort(node + ":" + port) }

func TestDNSNameTrimsDotAndCaches(t *testing.T) {
	c, f := newClient(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		got, err := c.DNSName(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got != node {
			t.Fatalf("DNSName = %q, want %q", got, node)
		}
	}
	if f.statuses != 1 {
		t.Errorf("status called %d times, want 1 (cached)", f.statuses)
	}
	url, err := c.URL(ctx, 5173)
	if err != nil || url != "https://"+node+":5173/" {
		t.Errorf("URL = %q, %v", url, err)
	}
}

func TestDNSNameMissing(t *testing.T) {
	c, f := newClient(t)
	f.dnsName = ""
	if _, err := c.DNSName(context.Background()); err == nil || !strings.Contains(err.Error(), "no MagicDNS name") {
		t.Fatalf("err = %v, want a missing MagicDNS name error", err)
	}
	// Nothing was cached, so a node that gets its name later is picked up.
	f.dnsName = node + "."
	if got, err := c.DNSName(context.Background()); err != nil || got != node {
		t.Fatalf("DNSName = %q, %v", got, err)
	}
}

func TestDNSNameUnreachable(t *testing.T) {
	c := New()
	c.lc.OmitAuth = true
	c.lc.Dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "unix", Err: io.ErrUnexpectedEOF}
	}
	if _, err := c.DNSName(context.Background()); err == nil || !strings.Contains(err.Error(), "tailscale status") {
		t.Fatalf("err = %v, want a tailscale status error", err)
	}
}

func TestPublishBatchesIntoOneWrite(t *testing.T) {
	c, f := newClient(t)
	if err := c.Publish(context.Background(), []int{3000, 5173}); err != nil {
		t.Fatal(err)
	}
	if f.gets != 1 || f.sets != 1 {
		t.Errorf("gets=%d sets=%d, want one read and one write for the batch", f.gets, f.sets)
	}
	sc := f.config(t)
	for _, port := range []int{3000, 5173} {
		h := sc.TCP[uint16(port)]
		if h == nil || !h.HTTPS {
			t.Errorf("port %d: TCP handler = %+v, want HTTPS", port, h)
		}
		web := sc.Web[ipn.HostPort(node+":"+strconv.Itoa(port))]
		if web == nil || web.Handlers["/"] == nil || web.Handlers["/"].Proxy != "http://127.0.0.1:"+strconv.Itoa(port) {
			t.Errorf("port %d: web = %+v, want a proxy to 127.0.0.1", port, web)
		}
	}
	if len(sc.AllowFunnel) != 0 {
		t.Errorf("AllowFunnel = %v: publishing must never use Funnel", sc.AllowFunnel)
	}
}

func TestPublishKeepsOtherMappings(t *testing.T) {
	c, f := newClient(t)
	f.set(t, &ipn.ServeConfig{
		TCP: map[uint16]*ipn.TCPPortHandler{443: {HTTPS: true}},
		Web: map[ipn.HostPort]*ipn.WebServerConfig{
			hp("443"): {Handlers: map[string]*ipn.HTTPHandler{"/": {Proxy: "http://127.0.0.1:8000"}}},
		},
	})
	if err := c.Publish(context.Background(), []int{3000}); err != nil {
		t.Fatal(err)
	}
	sc := f.config(t)
	if sc.TCP[443] == nil || sc.Web[hp("443")].Handlers["/"].Proxy != "http://127.0.0.1:8000" {
		t.Errorf("the mapping set up by hand changed: %+v", sc)
	}
	if sc.TCP[3000] == nil {
		t.Errorf("3000 not published: %+v", sc.TCP)
	}
}

func TestWithdrawLeavesTheRestAlone(t *testing.T) {
	c, f := newClient(t)
	ctx := context.Background()
	if err := c.Publish(ctx, []int{3000, 5173}); err != nil {
		t.Fatal(err)
	}
	// Funnel on 3000, as a user might add by hand: withdrawing the port must
	// not leave the Funnel flag behind for whatever binds it next.
	sc := f.config(t)
	sc.AllowFunnel = map[ipn.HostPort]bool{hp("3000"): true}
	f.set(t, sc)

	if err := c.Withdraw(ctx, []int{3000}); err != nil {
		t.Fatal(err)
	}
	sc = f.config(t)
	if sc.TCP[3000] != nil || sc.Web[hp("3000")] != nil || sc.AllowFunnel != nil {
		t.Errorf("3000 not fully withdrawn: %+v", sc)
	}
	if sc.TCP[5173] == nil || sc.Web[hp("5173")] == nil {
		t.Errorf("5173 withdrawn too: %+v", sc)
	}
}

func TestWithdrawLastPortLeavesConfigClean(t *testing.T) {
	c, f := newClient(t)
	ctx := context.Background()
	if err := c.Publish(ctx, []int{3000}); err != nil {
		t.Fatal(err)
	}
	if err := c.Withdraw(ctx, []int{3000}); err != nil {
		t.Fatal(err)
	}
	sc := f.config(t)
	if sc.TCP != nil || sc.Web != nil || sc.AllowFunnel != nil {
		t.Errorf("config not clean after the last withdraw: %+v", sc)
	}
	var raw map[string]any
	json.Unmarshal(f.serve, &raw)
	for _, key := range []string{"TCP", "Web", "AllowFunnel"} {
		if _, ok := raw[key]; ok {
			t.Errorf("%s still present in %s", key, f.serve)
		}
	}
}

func TestNoPortsMeansNoRoundTrip(t *testing.T) {
	c, f := newClient(t)
	ctx := context.Background()
	if err := c.Publish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Withdraw(ctx, []int{}); err != nil {
		t.Fatal(err)
	}
	if f.statuses+f.gets+f.sets != 0 {
		t.Errorf("calls made for an empty batch: status=%d get=%d set=%d", f.statuses, f.gets, f.sets)
	}
}

// A write based on a config that changed since it was read is refused by
// tailscaled; the client must report that rather than retry over the change.
func TestStaleWriteIsRefused(t *testing.T) {
	c, f := newClient(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// Someone else wrote between our read and our write.
			f.mu.Lock()
			f.serve = []byte(`{"TCP":{"8080":{"HTTPS":true}}}`)
			f.mu.Unlock()
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c.lc.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}
	err := c.Publish(context.Background(), []int{3000})
	if err == nil || !strings.Contains(err.Error(), "set serve config") {
		t.Fatalf("err = %v, want the write refused", err)
	}
	if sc := f.config(t); sc.TCP[3000] != nil || sc.TCP[8080] == nil {
		t.Errorf("the other writer's change was overwritten: %+v", sc.TCP)
	}
}

func TestAccessDenied(t *testing.T) {
	c, f := newClient(t)
	f.deny = true
	ctx := context.Background()
	if err := c.Publish(ctx, []int{3000}); err == nil || !strings.Contains(err.Error(), "get serve config") {
		t.Errorf("Publish err = %v, want get serve config denied", err)
	}
	if _, err := c.Published(ctx); err == nil {
		t.Error("Published: want an error when access is denied")
	}
	if _, err := c.Serving(ctx); err == nil {
		t.Error("Serving: want an error when access is denied")
	}
}

func TestPublishedAndServing(t *testing.T) {
	c, f := newClient(t)
	ctx := context.Background()

	// An empty config, as tailscaled reports it before anything is served.
	got, err := c.Published(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("Published on an empty config = %v, %v", got, err)
	}
	serving, err := c.Serving(ctx)
	if err != nil || len(serving) != 0 {
		t.Fatalf("Serving on an empty config = %v, %v", serving, err)
	}

	f.set(t, &ipn.ServeConfig{
		TCP: map[uint16]*ipn.TCPPortHandler{
			3000: {HTTPS: true},
			443:  {HTTPS: true},
			2222: {TCPForward: "127.0.0.1:22"}, // not HTTPS: not a URL
		},
		Web: map[ipn.HostPort]*ipn.WebServerConfig{
			hp("3000"): {Handlers: map[string]*ipn.HTTPHandler{"/": {Proxy: "http://127.0.0.1:3000"}}},
			hp("443"):  {Handlers: map[string]*ipn.HTTPHandler{"/": {Text: "hello"}}},
		},
	})
	got, err = c.Published(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[int]bool{3000: true, 443: true}; !reflect.DeepEqual(got, want) {
		t.Errorf("Published = %v, want %v", got, want)
	}

	serving, err = c.Serving(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(serving, func(i, j int) bool { return serving[i].Port < serving[j].Port })
	want := []status.Published{{Port: 443, Proxy: ""}, {Port: 3000, Proxy: "http://127.0.0.1:3000"}}
	if !reflect.DeepEqual(serving, want) {
		t.Errorf("Serving = %+v, want %+v", serving, want)
	}
	if f.sets != 0 {
		t.Errorf("reading wrote the serve config %d times", f.sets)
	}
}
