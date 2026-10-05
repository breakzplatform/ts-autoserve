package status

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func sample() Report {
	return Build("laptop.example-tailnet.ts.net",
		[]Published{{Port: 8080, Proxy: "http://127.0.0.1:8080"}, {Port: 3000, Proxy: "http://127.0.0.1:3000"}},
		map[int]bool{3000: true},
		Service{Installed: true, Running: true, Detail: "pid 42", File: "/x/agent.plist"})
}

func TestBuildSortsAndMarksManaged(t *testing.T) {
	r := sample()
	if len(r.Endpoints) != 2 || r.Endpoints[0].Port != 3000 {
		t.Fatalf("endpoints not sorted by port: %+v", r.Endpoints)
	}
	if got := r.Endpoints[0].URL; got != "https://laptop.example-tailnet.ts.net:3000/" {
		t.Errorf("url = %q", got)
	}
	if !r.Endpoints[0].Managed || r.Endpoints[1].Managed {
		t.Errorf("managed flags wrong: %+v", r.Endpoints)
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
	ep := got["endpoints"].([]any)[0].(map[string]any)
	for _, key := range []string{"port", "url", "proxy", "managed"} {
		if _, ok := ep[key]; !ok {
			t.Errorf("missing endpoint key %q", key)
		}
	}
	if got["version"].(float64) != Version {
		t.Errorf("version = %v", got["version"])
	}
}

func TestJSONEmptyEndpointsIsArray(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, Build("n.ts.net", nil, nil, Service{})); err != nil {
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
		"node: laptop.example-tailnet.ts.net",
		"service: running (pid 42)",
		"https://laptop.example-tailnet.ts.net:3000/  ->  127.0.0.1:3000  (ts-autoserve)",
		"https://laptop.example-tailnet.ts.net:8080/  ->  127.0.0.1:8080  (manual)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	buf.Reset()
	WriteText(&buf, Build("n.ts.net", nil, nil, Service{}))
	if !strings.Contains(buf.String(), "published: nothing") || !strings.Contains(buf.String(), "not installed") {
		t.Errorf("empty text wrong:\n%s", buf.String())
	}
}
