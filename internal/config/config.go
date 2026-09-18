// Package config loads ts-autoserve's YAML configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/breakzplatform/ts-autoserve/internal/portset"
)

// Mode decides which listening ports get published.
type Mode string

const (
	// ModeDev publishes only the well-known dev-server ports.
	ModeDev Mode = "dev"
	// ModeAgent publishes only ports opened by a coding agent.
	ModeAgent Mode = "agent"
	// ModeBoth publishes a port that is either one. The default.
	ModeBoth Mode = "both"
	// ModeAll publishes every port in the range. Noisy: local infrastructure
	// (editor bridges, sync daemons, debug ports) gets published too.
	ModeAll Mode = "all"
)

// Config is the on-disk configuration.
type Config struct {
	Mode         Mode          `yaml:"mode"`
	Interval     time.Duration `yaml:"interval"`      // how often to poll
	Grace        int           `yaml:"grace"`         // polls a port may be missing before withdrawal
	DevPorts     []string      `yaml:"dev_ports"`     // ports/ranges treated as dev servers
	ExcludePorts []string      `yaml:"exclude_ports"` // never published, whatever the mode says
	PortRange    []string      `yaml:"port_range"`    // bounds for mode "all"
	AgentPattern string        `yaml:"agent_pattern"` // process names that count as coding agents
	EnvFile      string        `yaml:"env_file"`      // KEY=value file read into the environment at startup
	Notify       Notify        `yaml:"notify"`

	Dev     portset.Set    `yaml:"-"`
	Exclude portset.Set    `yaml:"-"`
	Range   portset.Set    `yaml:"-"`
	Agent   *regexp.Regexp `yaml:"-"`
}

// Notify configures where "port is up" messages go.
type Notify struct {
	Telegram Telegram `yaml:"telegram"`
	Webhook  Webhook  `yaml:"webhook"`
	Messages Messages `yaml:"messages"`
}

// Messages overrides the text sent for each kind of event, as Go templates.
// A nil field keeps the built-in text; an empty one mutes that kind of event.
type Messages struct {
	Up    *string `yaml:"up"`
	Down  *string `yaml:"down"`
	Start *string `yaml:"start"`
}

// Telegram posts to the Bot API. Token may come from TokenEnv instead of disk.
type Telegram struct {
	Enabled  bool   `yaml:"enabled"`
	Token    string `yaml:"token"`
	TokenEnv string `yaml:"token_env"`
	ChatID   string `yaml:"chat_id"`
}

// Webhook posts a small JSON body to any URL. Body replaces that JSON with a
// template of its own, for services that expect a different shape.
type Webhook struct {
	Enabled     bool   `yaml:"enabled"`
	URL         string `yaml:"url"`
	Body        string `yaml:"body"`
	ContentType string `yaml:"content_type"`
}

// Default is the configuration used when no file exists.
//
// The dev-port list is the interesting part: filtering by "does it answer
// HTTP?" does not work, because local infrastructure answers HTTP too.
func Default() Config {
	return Config{
		Mode:     ModeBoth,
		Interval: 5 * time.Second,
		Grace:    2,
		DevPorts: []string{
			"1234",        // Parcel
			"1313",        // Hugo
			"2368",        // Ghost
			"3000-3010",   // Next.js, CRA, Rails, Express
			"3333",        // Nx
			"4000",        // Phoenix, Jekyll
			"4173",        // Vite preview
			"4200",        // Angular
			"4321",        // Astro
			"4567",        // Sinatra
			"5001",        // Flask
			"5173-5183",   // Vite
			"5500",        // Live Server
			"6006",        // Storybook
			"7777",        // misc
			"8000-8010",   // Django, python -m http.server
			"8080-8090",   // Tomcat, generic proxies
			"8100",        // Ionic
			"8888",        // Jupyter
			"19000-19006", // Expo
		},
		ExcludePorts: []string{
			"5000", // macOS ControlCenter (AirPlay)
			"7000", // macOS ControlCenter (AirPlay)
			"9222", // Chrome remote debugging: publishing it hands over the browser
			"8384", // Syncthing UI
			"22000",
		},
		PortRange: []string{"3000-9999"},
		// \b around short names: "agy" would otherwise match "strategy".
		AgentPattern: `claude|codex|cursor|windsurf|antigravity|\bagy\b|aider|opencode|goose|devin|copilot`,
	}
}

// SearchPaths lists where the config is looked for, in order.
//
// ~/.config works on every platform, including macOS, because that is where
// people who use several machines keep their dotfiles. The platform's own
// config directory still wins on macOS if a file is actually there.
func SearchPaths() []string {
	var paths []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" {
			return
		}
		p := filepath.Join(dir, "ts-autoserve", "config.yaml")
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	add(os.Getenv("XDG_CONFIG_HOME"))
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".config"))
	}
	if dir, err := os.UserConfigDir(); err == nil {
		add(dir)
	}
	return paths
}

// DefaultPath is the config file to read when none was given: the first one
// that exists, or the first candidate so error messages name a sensible path.
func DefaultPath() string {
	paths := SearchPaths()
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if len(paths) > 0 {
		return paths[0]
	}
	return "config.yaml"
}

// Load reads path, filling in defaults for anything absent. A missing file is
// not an error: the defaults are meant to work unconfigured.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var file Config
		if err := yaml.Unmarshal(data, &file); err != nil {
			return cfg, fmt.Errorf("%s: %w", path, err)
		}
		cfg = merge(cfg, file)
	case !os.IsNotExist(err):
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return compile(cfg)
}

func merge(base, file Config) Config {
	if file.Mode != "" {
		base.Mode = file.Mode
	}
	if file.Interval != 0 {
		base.Interval = file.Interval
	}
	if file.Grace != 0 {
		base.Grace = file.Grace
	}
	if file.DevPorts != nil {
		base.DevPorts = file.DevPorts
	}
	if file.ExcludePorts != nil {
		base.ExcludePorts = file.ExcludePorts
	}
	if file.PortRange != nil {
		base.PortRange = file.PortRange
	}
	if file.AgentPattern != "" {
		base.AgentPattern = file.AgentPattern
	}
	base.EnvFile = file.EnvFile
	base.Notify = file.Notify
	return base
}

func compile(cfg Config) (Config, error) {
	var err error
	if cfg.Dev, err = portset.Parse(cfg.DevPorts); err != nil {
		return cfg, fmt.Errorf("dev_ports: %w", err)
	}
	if cfg.Exclude, err = portset.Parse(cfg.ExcludePorts); err != nil {
		return cfg, fmt.Errorf("exclude_ports: %w", err)
	}
	if cfg.Range, err = portset.Parse(cfg.PortRange); err != nil {
		return cfg, fmt.Errorf("port_range: %w", err)
	}
	if cfg.AgentPattern != "" {
		if cfg.Agent, err = regexp.Compile("(?i)" + cfg.AgentPattern); err != nil {
			return cfg, fmt.Errorf("agent_pattern: %w", err)
		}
	}
	switch cfg.Mode {
	case ModeDev, ModeAgent, ModeBoth, ModeAll:
	default:
		return cfg, fmt.Errorf("mode %q: want dev, agent, both or all", cfg.Mode)
	}
	if cfg.Grace < 1 {
		cfg.Grace = 1
	}
	return cfg, nil
}

// ParseMode turns a mode written on the command line into a Mode.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeDev, ModeAgent, ModeBoth, ModeAll:
		return m, nil
	default:
		return "", fmt.Errorf("mode %q: want dev, agent, both or all", s)
	}
}

// WriteMode records a mode in the config file at path, so a choice made once at
// install time survives.
//
// It writes a new file and never rewrites an existing one: the config is the
// user's, comments and all, and silently rewriting it is a worse surprise than
// being told to edit one line. Setting the mode already in the file is fine.
func WriteMode(path string, mode Mode) error {
	if _, err := ParseMode(string(mode)); err != nil {
		return err
	}
	switch existing, err := os.ReadFile(path); {
	case err == nil:
		var file Config
		if err := yaml.Unmarshal(existing, &file); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if file.Mode == mode || (file.Mode == "" && mode == Default().Mode) {
			return nil
		}
		if file.Mode == "" {
			return fmt.Errorf("%s exists and sets no mode (so the default, %s, applies); add \"mode: %s\" to it", path, Default().Mode, mode)
		}
		return fmt.Errorf("%s already sets mode: %s; edit it to change that", path, file.Mode)
	case !os.IsNotExist(err):
		return fmt.Errorf("read %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body := fmt.Sprintf(`# ts-autoserve configuration. Everything else has a default;
# see the README for the full set of keys.
#
#   dev    only the well-known dev-server ports
#   agent  only ports opened by a process a coding agent started
#   both   either of the two (the default)
#   all    every port in port_range -- noisy
mode: %s
`, mode)
	return os.WriteFile(path, []byte(body), 0o600)
}

// Token resolves the Telegram token from the config or its environment variable.
func (t Telegram) Resolve() string {
	if t.Token != "" {
		return t.Token
	}
	if t.TokenEnv != "" {
		return os.Getenv(t.TokenEnv)
	}
	return ""
}
