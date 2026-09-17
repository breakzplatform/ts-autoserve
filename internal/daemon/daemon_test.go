package daemon

import (
	"context"
	"fmt"
	"testing"

	"github.com/breakzplatform/ts-autoserve/internal/config"
	"github.com/breakzplatform/ts-autoserve/internal/discover"
	"github.com/breakzplatform/ts-autoserve/internal/notify"
)

type fakePub struct {
	published map[int]bool
	publishes []int
	withdraws []int
	failPort  int
	calls     int // Publish calls, to prove a batch is one round trip
}

func newFakePub() *fakePub { return &fakePub{published: map[int]bool{}} }

func (f *fakePub) Publish(_ context.Context, ports []int) error {
	f.calls++
	for _, port := range ports {
		if port == f.failPort {
			return fmt.Errorf("boom")
		}
	}
	for _, port := range ports {
		f.published[port] = true
		f.publishes = append(f.publishes, port)
	}
	return nil
}

func (f *fakePub) Withdraw(_ context.Context, ports []int) error {
	for _, port := range ports {
		delete(f.published, port)
		f.withdraws = append(f.withdraws, port)
	}
	return nil
}

func (f *fakePub) Published(context.Context) (map[int]bool, error) {
	out := map[int]bool{}
	for p := range f.published {
		out[p] = true
	}
	return out, nil
}

func (f *fakePub) URL(_ context.Context, port int) (string, error) {
	return fmt.Sprintf("https://node.example.ts.net:%d/", port), nil
}

type fakeSource struct{ ports []int }

func (fakeSource) Name() string { return "fake" }

func (f *fakeSource) Listeners(context.Context) ([]discover.Listener, error) {
	out := make([]discover.Listener, 0, len(f.ports))
	for _, p := range f.ports {
		out = append(out, discover.Listener{Port: p, PID: 0, Proc: "test", Source: "fake"})
	}
	return out, nil
}

type recorder struct{ events []notify.Event }

func (r *recorder) Notify(_ context.Context, ev notify.Event) error {
	r.events = append(r.events, ev)
	return nil
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load("testdata-does-not-exist.yaml")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Mode = config.ModeDev
	return cfg
}

func TestPublishesDevPortAndWithdrawsAfterGrace(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	src := &fakeSource{ports: []int{3000}}
	pub := newFakePub()
	rec := &recorder{}
	d := New(cfg, []discover.Source{src}, pub, []notify.Notifier{rec})

	// First pass adopts what is already running: published, announced as one
	// startup summary rather than a per-port message.
	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !pub.published[3000] {
		t.Fatalf("port 3000 not published")
	}
	if len(rec.events) != 1 || rec.events[0].Kind != "start" {
		t.Fatalf("events = %+v, want one start event", rec.events)
	}

	// A second server appears: that one is announced.
	src.ports = []int{3000, 8080}
	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := rec.events[len(rec.events)-1]; got.Kind != "up" || got.Port != 8080 {
		t.Fatalf("last event = %+v, want up on 8080", got)
	}

	// It goes away: nothing happens until grace polls have passed.
	src.ports = []int{3000}
	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !pub.published[8080] {
		t.Fatalf("port 8080 withdrawn before grace elapsed")
	}
	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if pub.published[8080] {
		t.Fatalf("port 8080 still published after grace elapsed")
	}
}

func TestExcludedPortIsNeverPublished(t *testing.T) {
	cfg := testConfig(t)
	cfg.Mode = config.ModeAll
	src := &fakeSource{ports: []int{9222, 3000}}
	pub := newFakePub()
	d := New(cfg, []discover.Source{src}, pub, nil)

	if err := d.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if pub.published[9222] {
		t.Errorf("excluded port 9222 was published")
	}
	if !pub.published[3000] {
		t.Errorf("port 3000 was not published")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	cfg := testConfig(t)
	src := &fakeSource{ports: []int{3000}}
	pub := newFakePub()
	d := New(cfg, []discover.Source{src}, pub, nil)
	d.DryRun = true

	if err := d.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(pub.publishes) != 0 {
		t.Errorf("dry run published %v", pub.publishes)
	}
}

func TestFailedPublishIsRetriedNextPoll(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	src := &fakeSource{ports: []int{3000}}
	pub := newFakePub()
	pub.failPort = 3000
	d := New(cfg, []discover.Source{src}, pub, nil)

	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	pub.failPort = 0
	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !pub.published[3000] {
		t.Errorf("port 3000 never published after the failure cleared")
	}
}

func TestReclaimDropsStaleMappingsButKeepsLiveOnes(t *testing.T) {
	cfg := testConfig(t)
	src := &fakeSource{ports: []int{3000}}
	pub := newFakePub()
	pub.published[3000] = true // still listening: ours to re-adopt
	pub.published[4444] = true // gone: stale from an earlier run
	d := New(cfg, []discover.Source{src}, pub, nil)

	if err := d.reclaim(context.Background()); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if !pub.published[3000] {
		t.Errorf("live port 3000 was withdrawn")
	}
	if pub.published[4444] {
		t.Errorf("stale port 4444 survived reclaim")
	}
}

func TestReclaimKeepsAMappingForAPortPolicyWouldNotPublish(t *testing.T) {
	cfg := testConfig(t)
	src := &fakeSource{ports: []int{9999}} // listening, but not a dev port
	pub := newFakePub()
	pub.published[9999] = true // the user served it by hand
	d := New(cfg, []discover.Source{src}, pub, nil)

	if err := d.reclaim(context.Background()); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if !pub.published[9999] {
		t.Errorf("a hand-made mapping for a live port was withdrawn")
	}
}

func TestMappingFoundAtStartupIsNeverAdoptedOrWithdrawn(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	src := &fakeSource{ports: []int{3000}}
	pub := newFakePub()
	pub.published[3000] = true // ours or the user's -- ServeConfig does not say
	d := New(cfg, []discover.Source{src}, pub, nil)

	if err := d.reclaim(ctx); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(pub.publishes) != 0 {
		t.Errorf("published over a mapping it did not create: %v", pub.publishes)
	}

	// The server stops. The mapping is not the daemon's to clean up.
	src.ports = nil
	for i := 0; i < cfg.Grace+2; i++ {
		if err := d.Poll(ctx); err != nil {
			t.Fatalf("Poll: %v", err)
		}
	}
	if !pub.published[3000] {
		t.Errorf("withdrew a mapping it did not create")
	}
}

func TestMappingMadeWhileRunningIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	src := &fakeSource{ports: nil}
	pub := newFakePub()
	d := New(cfg, []discover.Source{src}, pub, nil)
	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	// The user runs `tailscale serve 3000` and starts a server on it.
	pub.published[3000] = true
	src.ports = []int{3000}
	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(pub.publishes) != 0 {
		t.Errorf("published over a mapping made while running: %v", pub.publishes)
	}

	src.ports = nil
	for i := 0; i < cfg.Grace+2; i++ {
		if err := d.Poll(ctx); err != nil {
			t.Fatalf("Poll: %v", err)
		}
	}
	if !pub.published[3000] {
		t.Errorf("withdrew a mapping made while running")
	}
}

func TestSeveralNewPortsArePublishedInOneCall(t *testing.T) {
	cfg := testConfig(t)
	src := &fakeSource{ports: []int{3000, 3001, 5173, 5174}}
	pub := newFakePub()
	d := New(cfg, []discover.Source{src}, pub, nil)

	if err := d.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if pub.calls != 1 {
		t.Errorf("Publish called %d times, want 1 batch", pub.calls)
	}
	if len(pub.publishes) != 4 {
		t.Errorf("published %v, want all four ports", pub.publishes)
	}
}
