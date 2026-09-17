// Package notify delivers "port is up" messages.
//
// The point of the daemon is developing from a phone, so the URL has to arrive
// where the phone is. Telegram is built in; anything else goes through the
// webhook and its JSON body.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/breakzplatform/ts-autoserve/internal/config"
)

// Event is one change worth telling the user about.
type Event struct {
	Kind   string `json:"kind"` // "up", "down" or "start"
	Port   int    `json:"port"`
	URL    string `json:"url,omitempty"`
	Proc   string `json:"proc,omitempty"`
	Source string `json:"source,omitempty"`
	Text   string `json:"text"`
}

// Notifier delivers events. A failure to notify never stops the daemon.
type Notifier interface {
	Notify(ctx context.Context, ev Event) error
}

// FromConfig builds the notifiers named in the config.
func FromConfig(cfg config.Notify) []Notifier {
	var out []Notifier
	if cfg.Telegram.Enabled {
		if token := cfg.Telegram.Resolve(); token != "" && cfg.Telegram.ChatID != "" {
			out = append(out, &telegram{token: token, chatID: cfg.Telegram.ChatID})
		} else {
			slog.Warn("telegram notifier enabled but token or chat_id is missing")
		}
	}
	if cfg.Webhook.Enabled && cfg.Webhook.URL != "" {
		out = append(out, &webhook{url: cfg.Webhook.URL})
	}
	return out
}

// All fans an event out, logging rather than propagating failures.
func All(ctx context.Context, ns []Notifier, ev Event) {
	for _, n := range ns {
		if err := n.Notify(ctx, ev); err != nil {
			slog.Warn("notify failed", "err", err)
		}
	}
}

var client = &http.Client{Timeout: 10 * time.Second}

type telegram struct {
	token  string
	chatID string
}

func (t *telegram) Notify(ctx context.Context, ev Event) error {
	form := url.Values{
		"chat_id": {t.chatID},
		"text":    {ev.Text},
	}
	// Withdrawals are housekeeping: deliver them without buzzing the phone.
	if ev.Kind != "up" {
		form.Set("disable_notification", "true")
	}
	endpoint := "https://api.telegram.org/bot" + t.token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		// The token must not reach the logs through the URL in the error.
		return fmt.Errorf("telegram request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("telegram returned %s", resp.Status)
	}
	return nil
}

type webhook struct{ url string }

func (w *webhook) Notify(ctx context.Context, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}
