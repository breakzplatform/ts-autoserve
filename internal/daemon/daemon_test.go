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

// fakeStore stands in for the state file: what the last run published.
type fakeStore struct {
	ports []int
	saved []int
	saves int
	err   error
}

func (f *fakeStore) Load() (map[int]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[int]bool{}
	for _, p := range f.ports {
		out[p] = true
	}
	return out, nil
}

func (f *fakeStore) Save(ports []int) error {
	f.saved = append([]int(nil), ports...)
	f.saves++
	return nil
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

func TestReclaimDropsOurStaleMappingsAndReAdoptsOurLiveOnes(t *testing.T) {
	cfg := testConfig(t)
	src := &fakeSource{ports: []int{3000}}
	pub := newFakePub()
	pub.published[3000] = true // still listening: ours to re-adopt
	pub.published[4444] = true // gone: stale from an earlier run
	d := New(cfg, []discover.Source{src}, pub, nil)
	d.Store = &fakeStore{ports: []int{3000, 4444}}

	if err := d.reclaim(context.Background()); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if !pub.published[3000] {
		t.Errorf("live port 3000 was withdrawn")
	}
	if d.owned[3000] == nil {
		t.Errorf("live port 3000 was not re-adopted")
	}
	if pub.published[4444] {
		t.Errorf("stale port 4444 survived reclaim")
	}
}

func TestReclaimKeepsAPersistentMappingWhoseServerIsDown(t *testing.T) {
	cfg := testConfig(t)
	src := &fakeSource{ports: nil} // nothing is listening: fresh boot
	pub := newFakePub()
	pub.published[8000] = true // `tailscale serve 8000`, set up to live there
	d := New(cfg, []discover.Source{src}, pub, nil)
	d.Store = &fakeStore{} // this daemon published nothing last run

	if err := d.reclaim(context.Background()); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if !pub.published[8000] {
		t.Fatalf("deleted a mapping the daemon never made")
	}

	// And it stays out of reach once the user's server does come up.
	src.ports = []int{8000}
	if err := d.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(pub.publishes) != 0 {
		t.Errorf("published over it once its server appeared: %v", pub.publishes)
	}
}

func TestReAdoptedPortIsWithdrawnWhenItsServerStops(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	src := &fakeSource{ports: []int{3000}}
	pub := newFakePub()
	pub.published[3000] = true
	d := New(cfg, []discover.Source{src}, pub, nil)
	d.Store = &fakeStore{ports: []int{3000}}

	if err := d.reclaim(ctx); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	src.ports = nil
	for i := 0; i < cfg.Grace+1; i++ {
		if err := d.Poll(ctx); err != nil {
			t.Fatalf("Poll: %v", err)
		}
	}
	if pub.published[3000] {
		t.Errorf("a mapping this daemon owns outlived its server")
	}
}

func TestReclaimDropsOurMappingForAPortNowExcluded(t *testing.T) {
	cfg := testConfig(t)
	cfg.Mode = config.ModeAll
	src := &fakeSource{ports: []int{9222}} // listening, but excluded by config
	pub := newFakePub()
	pub.published[9222] = true
	d := New(cfg, []discover.Source{src}, pub, nil)
	d.Store = &fakeStore{ports: []int{9222}}

	if err := d.reclaim(context.Background()); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if pub.published[9222] {
		t.Errorf("an exclusion added since the last run was not applied")
	}
}

func TestUnreadableStateWithdrawsNothing(t *testing.T) {
	cfg := testConfig(t)
	src := &fakeSource{ports: nil}
	pub := newFakePub()
	pub.published[3000] = true
	d := New(cfg, []discover.Source{src}, pub, nil)
	d.Store = &fakeStore{err: fmt.Errorf("corrupt")}

	if err := d.reclaim(context.Background()); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if !pub.published[3000] {
		t.Errorf("withdrew a mapping without knowing whose it was")
	}
}

func TestStateFollowsWhatIsPublished(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	src := &fakeSource{ports: []int{3000}}
	pub := newFakePub()
	store := &fakeStore{}
	d := New(cfg, []discover.Source{src}, pub, nil)
	d.Store = store

	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(store.saved) != 1 || store.saved[0] != 3000 {
		t.Fatalf("state = %v, want [3000]", store.saved)
	}

	src.ports = nil
	for i := 0; i < cfg.Grace+1; i++ {
		if err := d.Poll(ctx); err != nil {
			t.Fatalf("Poll: %v", err)
		}
	}
	if len(store.saved) != 0 {
		t.Fatalf("state = %v, want empty once the port is withdrawn", store.saved)
	}

	// A poll that changes nothing does not rewrite the file.
	saves := store.saves
	if err := d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if store.saves != saves {
		t.Errorf("state written on a poll that changed nothing")
	}
}

func TestShutdownClearsTheState(t *testing.T) {
	cfg := testConfig(t)
	src := &fakeSource{ports: []int{3000}}
	pub := newFakePub()
	store := &fakeStore{}
	d := New(cfg, []discover.Source{src}, pub, nil)
	d.Store = store

	if err := d.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	d.shutdown()
	if pub.published[3000] {
		t.Errorf("shutdown left port 3000 published")
	}
	if len(store.saved) != 0 {
		t.Errorf("state = %v, want empty after shutdown", store.saved)
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

type pidSource struct{ listeners []discover.Listener }

func (pidSource) Name() string { return "fake" }

func (f *pidSource) Listeners(context.Context) ([]discover.Listener, error) {
	return f.listeners, nil
}

func TestAncestryIsWalkedOncePerProcess(t *testing.T) {
	cfg := testConfig(t)
	cfg.Mode = config.ModeAgent
	src := &pidSource{listeners: []discover.Listener{
		{Port: 4000, PID: 100, Proc: "node", Source: "fake"},
		{Port: 4001, PID: 200, Proc: "rapportd", Source: "fake"},
	}}
	pub := newFakePub()
	d := New(cfg, []discover.Source{src}, pub, nil)
	walks := map[int]int{}
	d.spawned = func(pid int) bool { walks[pid]++; return pid == 100 }

	poll := func() {
		t.Helper()
		if err := d.Poll(context.Background()); err != nil {
			t.Fatalf("Poll: %v", err)
		}
	}
	for range 3 {
		poll()
	}
	if walks[100] != 1 || walks[200] != 1 {
		t.Fatalf("walks = %v, want each process walked once", walks)
	}
	if !pub.published[4000] || pub.published[4001] {
		t.Fatalf("published %v, want only 4000", pub.published)
	}

	// The same pid now names another process: judged afresh.
	src.listeners[1].Proc = "vite"
	poll()
	if walks[200] != 2 {
		t.Errorf("reused pid walked %d times, want 2", walks[200])
	}

	// A process that stops listening is forgotten, so coming back is a new walk.
	src.listeners = src.listeners[:1]
	poll()
	src.listeners = append(src.listeners, discover.Listener{Port: 4001, PID: 200, Proc: "vite", Source: "fake"})
	poll()
	if walks[200] != 3 {
		t.Errorf("returning process walked %d times, want 3", walks[200])
	}
}
