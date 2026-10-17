package status

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func sample() Report {
	return Build("laptop.example-tailnet.ts.net",
		[]Published{
			{Port: 8080, Proxy: "http://127.0.0.1:8080"},
			{Port: 3000, Proxy: "http://127.0.0.1:3000"},
			{Port: 443, Proxy: "http://localhost:5173"},
		},
		map[int]bool{3000: true},
		map[int]Process{3000: {PID: 812, Name: "node"}, 5173: {PID: 913, Name: "vite"}},
		Service{Installed: true, Running: true, Detail: "pid 42", File: "/x/agent.plist"})
}

func TestBuildSortsAndMarksManaged(t *testing.T) {
	r := sample()
	if len(r.Endpoints) != 3 || r.Endpoints[0].Port != 443 || r.Endpoints[1].Port != 3000 {
		t.Fatalf("endpoints not sorted by port: %+v", r.Endpoints)
	}
	if got := r.Endpoints[1].URL; got != "https://laptop.example-tailnet.ts.net:3000/" {
		t.Errorf("url = %q", got)
	}
	if !r.Endpoints[1].Managed || r.Endpoints[0].Managed || r.Endpoints[2].Managed {
		t.Errorf("managed flags wrong: %+v", r.Endpoints)
	}
}

func TestBuildAttachesProcessOfProxyTarget(t *testing.T) {
	r := sample()
	byPort := map[int]Endpoint{}
	for _, e := range r.Endpoints {
		byPort[e.Port] = e
	}
	if p := byPort[3000].Process; p == nil || *p != (Process{PID: 812, Name: "node"}) {
		t.Errorf("3000 process = %+v, want node 812", p)
	}
	// Served on 443, forwarding to 5173: the process is the one on 5173.
	if p := byPort[443].Process; p == nil || p.Name != "vite" {
		t.Errorf("443 process = %+v, want the listener on 5173", p)
	}
	if p := byPort[8080].Process; p != nil {
		t.Errorf("8080 process = %+v, want nil: nothing listens there", p)
	}
}

func TestBuildNoProcessForRemoteOrNonProxyTargets(t *testing.T) {
	listening := map[int]Process{3000: {PID: 1, Name: "x"}}
	r := Build("n.ts.net", []Published{
		{Port: 3000, Proxy: "http://192.168.1.20:3000"},
		{Port: 3001, Proxy: ""},
	}, nil, listening, Service{})
	for _, e := range r.Endpoints {
		if e.Process != nil {
			t.Errorf("port %d: process = %+v, want nil", e.Port, e.Process)
		}
	}
}

func TestLocalTarget(t *testing.T) {
	for proxy, want := range map[string]int{
		"http://127.0.0.1:3000":         3000,
		"http://localhost:5173":         5173,
		"https+insecure://[::1]:8443":   8443,
		"127.0.0.1:4200":                4200,
		"http://[::ffff:127.0.0.1]:808": 808,
	} {
		if got, ok := LocalTarget(proxy); !ok || got != want {
			t.Errorf("LocalTarget(%q) = %d, %v; want %d", proxy, got, ok, want)
		}
	}
	for _, proxy := range []string{
		"",
		"http://192.168.1.20:3000",
		"http://example.com:3000",
		"http://127.0.0.1",
		"http://127.0.0.1:0",
		"text:hello",
	} {
		if got, ok := LocalTarget(proxy); ok {
			t.Errorf("LocalTarget(%q) = %d, want rejected", proxy, got)
		}
	}
}

func TestJSONShape(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "node", "endpoints", "service"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing top-level key %q", key)
		}
	}
	if got["version"].(float64) != 1 {
		t.Errorf("version = %v, want 1: adding fields must not bump it", got["version"])
	}
	eps := got["endpoints"].([]any)
	ep := eps[1].(map[string]any) // port 3000
	// The v1 keys first shipped, plus process, added without a bump.
	for _, key := range []string{"port", "url", "proxy", "managed", "process"} {
		if _, ok := ep[key]; !ok {
			t.Errorf("missing endpoint key %q", key)
		}
	}
	proc, ok := ep["process"].(map[string]any)
	if !ok || proc["pid"].(float64) != 812 || proc["name"] != "node" {
		t.Errorf("process = %v, want pid 812 name node", ep["process"])
	}
	// Nothing listening is null, not a zero object, so it reads differently
	// from a listener whose owner the OS would not name.
	if p, ok := eps[2].(map[string]any)["process"]; !ok || p != nil {
		t.Errorf("8080 process = %v (present %v), want null", p, ok)
	}
}

// A v1 consumer written against the first release must still decode the
// report, whatever was added since.
func TestJSONDecodesIntoV1Shape(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	var v1 struct {
		Version   int    `json:"version"`
		Node      string `json:"node"`
		Endpoints []struct {
			Port    int    `json:"port"`
			URL     string `json:"url"`
			Proxy   string `json:"proxy"`
			Managed bool   `json:"managed"`
		} `json:"endpoints"`
		Service struct {
			Installed bool   `json:"installed"`
			Running   bool   `json:"running"`
			Detail    string `json:"detail"`
			File      string `json:"file"`
		} `json:"service"`
	}
	dec := json.NewDecoder(&buf)
	if err := dec.Decode(&v1); err != nil {
		t.Fatal(err)
	}
	if v1.Version != 1 || len(v1.Endpoints) != 3 || v1.Endpoints[1].Proxy != "http://127.0.0.1:3000" || !v1.Service.Running {
		t.Errorf("v1 decode = %+v", v1)
	}
}

func TestJSONEmptyEndpointsIsArray(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, Build("n.ts.net", nil, nil, nil, Service{})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"endpoints": []`) {
		t.Errorf("want empty array, got:\n%s", buf.String())
	}
}

func TestWriteText(t *testing.T) {
	var buf bytes.Buffer
	WriteText(&buf, sample())
	out := buf.String()
	for _, want := range []string{
		"node: laptop.example-tailnet.ts.net\n",
		"service: running (pid 42)\n",
		"  https://laptop.example-tailnet.ts.net:443/   ->  localhost:5173  (manual)        vite (pid 913)\n",
		"  https://laptop.example-tailnet.ts.net:3000/  ->  127.0.0.1:3000  (ts-autoserve)  node (pid 812)\n",
		"  https://laptop.example-tailnet.ts.net:8080/  ->  127.0.0.1:8080  (manual)        not listening\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	buf.Reset()
	WriteText(&buf, Build("n.ts.net", nil, nil, nil, Service{}))
	if !strings.Contains(buf.String(), "published: nothing") || !strings.Contains(buf.String(), "not installed") {
		t.Errorf("empty text wrong:\n%s", buf.String())
	}
}

func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		p    *Process
		want string
	}{
		{nil, "not listening"},
		{&Process{PID: 7, Name: "node"}, "node (pid 7)"},
		{&Process{PID: 7}, "pid 7"},
		{&Process{Name: "node"}, "node"},
		{&Process{}, "listening, owner unknown"},
	} {
		if got := describe(c.p); got != c.want {
			t.Errorf("describe(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}
