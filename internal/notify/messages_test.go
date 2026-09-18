package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/breakzplatform/ts-autoserve/internal/config"
)

func ptr(s string) *string { return &s }

func TestDefaultMessages(t *testing.T) {
	var m *Messages // nil: built-in text
	for _, tc := range []struct {
		ev   Event
		want string
	}{
		{Event{Kind: "up", Port: 5173, Proc: "node", URL: "https://n.ts.net:5173/"}, "node up on port 5173\nhttps://n.ts.net:5173/"},
		{Event{Kind: "up", Port: 3000, URL: "https://n.ts.net:3000/"}, "a local server up on port 3000\nhttps://n.ts.net:3000/"},
		{Event{Kind: "down", Port: 5173}, "port 5173 is gone"},
		{Event{Kind: "start", Ports: []int{3000, 5173}}, "ts-autoserve is up; already serving 3000, 5173"},
	} {
		ev := tc.ev
		if !m.Render(&ev) || ev.Text != tc.want {
			t.Errorf("%s: text = %q, want %q", ev.Kind, ev.Text, tc.want)
		}
	}
}

func TestCustomMessagesAndMuting(t *testing.T) {
	m, err := ParseMessages(config.Messages{
		Up:   ptr("🟢 {{.Proc}} → {{.URL}}"),
		Down: ptr(""),
	})
	if err != nil {
		t.Fatalf("ParseMessages: %v", err)
	}
	up := Event{Kind: "up", Port: 5173, Proc: "vite", URL: "https://n/"}
	if !m.Render(&up) || up.Text != "🟢 vite → https://n/" {
		t.Errorf("up text = %q", up.Text)
	}
	down := Event{Kind: "down", Port: 5173}
	if m.Render(&down) {
		t.Errorf("an empty template should mute the event, got %q", down.Text)
	}
	start := Event{Kind: "start", Ports: []int{3000}}
	if !m.Render(&start) || start.Text != "ts-autoserve is up; already serving 3000" {
		t.Errorf("start should keep the built-in text, got %q", start.Text)
	}
}

func TestParseMessagesRejectsBadTemplate(t *testing.T) {
	if _, err := ParseMessages(config.Messages{Up: ptr("{{.Port")}); err == nil {
		t.Error("ParseMessages accepted a broken template")
	}
}

func TestWebhookBodyTemplate(t *testing.T) {
	var body, ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body, ct = string(b), r.Header.Get("Content-Type")
	}))
	defer srv.Close()

	ns, err := FromConfig(config.Notify{Webhook: config.Webhook{
		Enabled: true,
		URL:     srv.URL,
		Body:    `{"content": {{json .Text}}}`,
	}})
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if err := ns[0].Notify(context.Background(), Event{Kind: "up", Text: "say \"hi\"\nthere"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if want := `{"content": "say \"hi\"\nthere"}`; body != want {
		t.Errorf("body = %s, want %s", body, want)
	}
	if ct != "application/json" {
		t.Errorf("content type = %q", ct)
	}
}

func TestFromConfigRejectsBadBodyTemplate(t *testing.T) {
	_, err := FromConfig(config.Notify{Webhook: config.Webhook{Enabled: true, URL: "http://x", Body: "{{"}})
	if err == nil {
		t.Error("FromConfig accepted a broken body template")
	}
}
