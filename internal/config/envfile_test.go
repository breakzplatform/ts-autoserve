package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	body := `# tokens
export TSA_BARE=abc123
TSA_DOUBLE="with space"
TSA_SINGLE='$NOT_EXPANDED'
TSA_EXPANDED="${TSA_BARE}/x"
TSA_COMMENT=value # trailing
TSA_ALREADY=from-file

`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TSA_ALREADY", "from-env")
	for _, k := range []string{"TSA_BARE", "TSA_DOUBLE", "TSA_SINGLE", "TSA_EXPANDED", "TSA_COMMENT"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	if err := ApplyEnvFile(path); err != nil {
		t.Fatalf("ApplyEnvFile: %v", err)
	}
	for k, want := range map[string]string{
		"TSA_BARE":     "abc123",
		"TSA_DOUBLE":   "with space",
		"TSA_SINGLE":   "$NOT_EXPANDED",
		"TSA_EXPANDED": "abc123/x",
		"TSA_COMMENT":  "value",
		"TSA_ALREADY":  "from-env",
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestApplyEnvFileRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	os.WriteFile(path, []byte("not a variable\n"), 0o600)
	if err := ApplyEnvFile(path); err == nil {
		t.Error("ApplyEnvFile accepted a line with no =")
	}
}

func TestApplyEnvFileMissingIsAnError(t *testing.T) {
	// Asked for by name: a typo should not silently leave the token unset.
	if err := ApplyEnvFile(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("ApplyEnvFile accepted a missing file")
	}
}

func TestMessagesDistinguishUnsetFromEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("notify:\n  messages:\n    down: \"\"\n    up: \"{{.Port}}\"\n"), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := cfg.Notify.Messages
	if m.Start != nil {
		t.Errorf("start = %q, want unset", *m.Start)
	}
	if m.Down == nil || *m.Down != "" {
		t.Errorf("down = %v, want set and empty", m.Down)
	}
	if m.Up == nil || *m.Up != "{{.Port}}" {
		t.Errorf("up = %v, want the template", m.Up)
	}
}
