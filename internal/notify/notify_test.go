package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/breakzplatform/ts-autoserve/internal/config"
)

func TestTelegramPostsMessage(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		got = r.PostForm
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tg := &telegram{token: "secret", chatID: "42", endpoint: srv.URL}
	ev := Event{Kind: "up", Port: 5173, Text: "node up on port 5173"}
	if err := tg.Notify(context.Background(), ev); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got.Get("chat_id") != "42" {
		t.Errorf("chat_id = %q, want 42", got.Get("chat_id"))
	}
	if got.Get("text") != ev.Text {
		t.Errorf("text = %q, want %q", got.Get("text"), ev.Text)
	}
	if got.Get("disable_notification") == "true" {
		t.Errorf("an 'up' event should buzz the phone")
	}
}

func TestTelegramKeepsHousekeepingSilent(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
	}))
	defer srv.Close()

	tg := &telegram{token: "secret", chatID: "42", endpoint: srv.URL}
	if err := tg.Notify(context.Background(), Event{Kind: "down", Port: 5173, Text: "gone"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got.Get("disable_notification") != "true" {
		t.Errorf("a 'down' event should be silent")
	}
}

func TestTelegramErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	tg := &telegram{token: "super-secret-token", chatID: "42", endpoint: srv.URL}
	err := tg.Notify(context.Background(), Event{Kind: "up", Text: "hi"})
	if err == nil {
		t.Fatal("Notify succeeded on a 401")
	}
	if contains(err.Error(), "super-secret-token") {
		t.Errorf("error message leaks the token: %v", err)
	}
}

func TestWebhookPostsJSON(t *testing.T) {
	var got Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
	}))
	defer srv.Close()

	w := &webhook{url: srv.URL}
	ev := Event{Kind: "up", Port: 3000, URL: "https://node.ts.net:3000/", Proc: "node", Text: "up"}
	if err := w.Notify(context.Background(), ev); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got != ev {
		t.Errorf("webhook got %+v, want %+v", got, ev)
	}
}

func TestFromConfigIsEmptyWhenNothingEnabled(t *testing.T) {
	if ns := FromConfig(config.Notify{}); len(ns) != 0 {
		t.Errorf("FromConfig returned %d notifiers for an empty config", len(ns))
	}
}

func TestFromConfigSkipsIncompleteTelegram(t *testing.T) {
	cfg := config.Notify{Telegram: config.Telegram{Enabled: true, Token: "x"}} // no chat_id
	if ns := FromConfig(cfg); len(ns) != 0 {
		t.Errorf("FromConfig accepted a telegram config with no chat_id")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}
