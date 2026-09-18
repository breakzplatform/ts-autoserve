package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/breakzplatform/ts-autoserve/internal/config"
)

// The built-in text for each kind of event. The config can replace any of them.
const (
	defaultUp    = "{{.Label}} up on port {{.Port}}\n{{.URL}}"
	defaultDown  = "port {{.Port}} is gone"
	defaultStart = "ts-autoserve is up; already serving {{join .Ports}}"
)

var funcs = template.FuncMap{
	// join lists ports the way a person would write them: "3000, 5173".
	"join": func(ports []int) string {
		parts := make([]string, len(ports))
		for i, p := range ports {
			parts[i] = fmt.Sprint(p)
		}
		return strings.Join(parts, ", ")
	},
	// json quotes a value for a webhook body template.
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
}

// Messages turns events into the text that gets sent. The zero value, and a
// nil *Messages, use the built-in text.
type Messages struct {
	byKind map[string]*template.Template
}

// ParseMessages compiles the templates in the config, so a typo is reported at
// startup rather than at the first notification.
func ParseMessages(cfg config.Messages) (*Messages, error) {
	m := &Messages{byKind: map[string]*template.Template{}}
	for kind, override := range map[string]*string{"up": cfg.Up, "down": cfg.Down, "start": cfg.Start} {
		if override == nil {
			continue
		}
		t, err := template.New(kind).Funcs(funcs).Parse(*override)
		if err != nil {
			return nil, fmt.Errorf("notify.messages.%s: %w", kind, err)
		}
		m.byKind[kind] = t
	}
	return m, nil
}

var defaults = map[string]*template.Template{
	"up":    template.Must(template.New("up").Funcs(funcs).Parse(defaultUp)),
	"down":  template.Must(template.New("down").Funcs(funcs).Parse(defaultDown)),
	"start": template.Must(template.New("start").Funcs(funcs).Parse(defaultStart)),
}

// Render fills in ev.Text. It reports false when the event should not be sent
// at all: its template was set to nothing, which is how a kind is muted.
func (m *Messages) Render(ev *Event) bool {
	t := defaults[ev.Kind]
	if m != nil && m.byKind[ev.Kind] != nil {
		t = m.byKind[ev.Kind]
	}
	if t == nil {
		return ev.Text != ""
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, ev); err != nil {
		// A template that parses can still fail on data; the built-in text is
		// a better message than none.
		buf.Reset()
		_ = defaults[ev.Kind].Execute(&buf, ev)
	}
	ev.Text = strings.TrimSpace(buf.String())
	return ev.Text != ""
}
