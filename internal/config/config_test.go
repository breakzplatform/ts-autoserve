package config

import "testing"

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
		"/Applications/Antigravity.app/Contents/MacOS/Antigravity",
	} {
		if !cfg.Agent.MatchString(cmd) {
			t.Errorf("agent pattern did not match %q", cmd)
		}
	}
}

func TestDefaultAgentPatternAvoidsSubstringTraps(t *testing.T) {
	// "agy" is short enough to appear inside ordinary words and paths.
	for _, cmd := range []string{
		"node /Users/me/strategy-dashboard/node_modules/.bin/vite",
		"/usr/bin/python3 /opt/legacy/server.py",
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
