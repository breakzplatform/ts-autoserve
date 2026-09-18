package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultAgentPatternMatchesAgents(t *testing.T) {
	cfg, err := Load("no-such-config.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, cmd := range []string{
		"/Users/me/.local/bin/claude",
		"node /opt/homebrew/bin/codex --resume",
		"/usr/local/bin/agy",
		"agy chat",
		"/opt/homebrew/bin/grok",
		"/Applications/Antigravity.app/Contents/MacOS/Antigravity",
	} {
		if !cfg.Agent.MatchString(cmd) {
			t.Errorf("agent pattern did not match %q", cmd)
		}
	}
}

func TestDefaultAgentPatternAvoidsSubstringTraps(t *testing.T) {
	// "agy" and "grok" are short enough to appear inside other names ("strategy", "ngrok").
	for _, cmd := range []string{
		"node /Users/me/strategy-dashboard/node_modules/.bin/vite",
		"/usr/bin/python3 /opt/legacy/server.py",
		"ngrok http 3000",
	} {
		if cfg, _ := Load("no-such-config.yaml"); cfg.Agent.MatchString(cmd) {
			t.Errorf("agent pattern wrongly matched %q", cmd)
		}
	}
}

func TestLoadRejectsBadMode(t *testing.T) {
	cfg := Default()
	cfg.Mode = "sometimes"
	if _, err := compile(cfg); err == nil {
		t.Error("compile accepted an unknown mode")
	}
}

func TestWriteModeCreatesAConfigThatLoadsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	if err := WriteMode(path, ModeAgent); err != nil {
		t.Fatalf("WriteMode: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Mode != ModeAgent {
		t.Errorf("mode = %q, want agent", cfg.Mode)
	}
	// The rest of the defaults must survive a config that only sets the mode.
	if !cfg.Dev.Has(5173) || !cfg.Exclude.Has(9222) {
		t.Errorf("writing a mode dropped the default port lists")
	}
}

func TestWriteModeNeverRewritesSomeoneElsesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "# mine\nmode: dev\ngrace: 7\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteMode(path, ModeAgent); err == nil {
		t.Error("WriteMode overwrote a config that sets another mode")
	}
	if err := WriteMode(path, ModeDev); err != nil {
		t.Errorf("WriteMode on a config already in that mode: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("config was rewritten:\n%s", got)
	}
}

func TestParseMode(t *testing.T) {
	for _, s := range []string{"dev", "agent", "both", "all"} {
		if _, err := ParseMode(s); err != nil {
			t.Errorf("ParseMode(%q): %v", s, err)
		}
	}
	if _, err := ParseMode("agents"); err == nil {
		t.Error("ParseMode accepted an unknown mode")
	}
}
