package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ApplyEnvFile reads KEY=value lines from path into the process environment,
// so a token can live in one file the user already keeps instead of being
// copied into a service definition.
//
// The format is the subset of shell that such files use: blank lines and #
// comments, an optional "export ", and values that are bare, 'single-quoted'
// (taken literally) or "double-quoted". Bare and double-quoted values expand
// $VAR and ${VAR}. A variable already set in the environment wins over the
// file, the way an explicit setting should. An empty path does nothing.
func ApplyEnvFile(path string) error {
	if path == "" {
		return nil
	}
	path = expandHome(path)
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("env_file: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return fmt.Errorf("env_file %s:%d: want KEY=value", path, n)
		}
		val, err := envValue(raw)
		if err != nil {
			return fmt.Errorf("env_file %s:%d: %w", path, n, err)
		}
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return fmt.Errorf("env_file %s:%d: %w", path, n, err)
		}
	}
	return sc.Err()
}

func envValue(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	switch q := raw[0]; q {
	case '\'', '"':
		end := strings.IndexByte(raw[1:], q)
		if end < 0 {
			return "", fmt.Errorf("unterminated %c quote", q)
		}
		val := raw[1 : end+1]
		if q == '\'' {
			return val, nil
		}
		return os.ExpandEnv(val), nil
	}
	// A bare value ends at a comment, as it would in the shell.
	if i := strings.Index(raw, " #"); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	return os.ExpandEnv(raw), nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}
