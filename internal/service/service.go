// Package service installs ts-autoserve as a user service.
//
// A launchd agent on macOS, a systemd user unit on Linux. Both start at login
// and restart on failure, so the daemon is running whenever the machine is —
// which is the only way "my dev server just appears on my phone" holds up.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Label identifies the service to the OS.
const Label = "com.breakzplatform.ts-autoserve"

// UnitName is the systemd unit file name.
const UnitName = "ts-autoserve.service"

// Install writes the service definition and starts it.
// binPath is the executable to run; env entries are passed to the service.
func Install(binPath string, env map[string]string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(binPath, env)
	case "linux":
		return installSystemd(binPath, env)
	default:
		return "", fmt.Errorf("service install is not supported on %s; run ts-autoserve from your own supervisor", runtime.GOOS)
	}
}

// Uninstall stops the service and removes its definition.
func Uninstall() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchd()
	case "linux":
		return uninstallSystemd()
	default:
		return "", fmt.Errorf("service uninstall is not supported on %s", runtime.GOOS)
	}
}

func installLaunchd(binPath string, env map[string]string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, Label+".plist")
	// 0600: the file may carry a notification token. WriteFile does not change
	// the mode of a file that already exists, so set it explicitly.
	if err := os.WriteFile(path, []byte(plist(binPath, env)), 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}

	target := "gui/" + strconv.Itoa(os.Getuid())
	// bootout first so reinstalling picks up a changed plist. bootout returns
	// before launchd has finished tearing the job down, and bootstrapping a
	// label that is still present fails with "Input/output error" — so wait for
	// the old job to disappear instead of racing it.
	_ = run("launchctl", "bootout", target+"/"+Label)
	waitGone(target + "/" + Label)

	var bootErr error
	for attempt := 0; attempt < 3; attempt++ {
		if bootErr = run("launchctl", "bootstrap", target, path); bootErr == nil {
			return path, nil
		}
		time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
	}
	return path, fmt.Errorf("launchctl bootstrap: %w", bootErr)
}

// waitGone blocks until launchd no longer knows the service, or two seconds pass.
func waitGone(target string) {
	for i := 0; i < 20; i++ {
		if err := run("launchctl", "print", target); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func uninstallLaunchd() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	_ = run("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+Label)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return path, err
	}
	return path, nil
}

func installSystemd(binPath string, env map[string]string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, UnitName)
	// 0600: the file may carry a notification token. WriteFile does not change
	// the mode of a file that already exists, so set it explicitly.
	if err := os.WriteFile(path, []byte(unit(binPath, env)), 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return path, fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if err := run("systemctl", "--user", "enable", "--now", UnitName); err != nil {
		return path, fmt.Errorf("systemctl enable: %w", err)
	}
	return path, nil
}

func uninstallSystemd() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".config", "systemd", "user", UnitName)
	_ = run("systemctl", "--user", "disable", "--now", UnitName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return path, err
	}
	_ = run("systemctl", "--user", "daemon-reload")
	return path, nil
}

// LingerHint explains how to keep a Linux user service running without a login
// session. Empty on other platforms, where login is not a precondition.
func LingerHint() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	user := os.Getenv("USER")
	if user == "" {
		user = "$USER"
	}
	return "a user service starts when you log in; for a headless machine run: sudo loginctl enable-linger " + user
}

func plist(binPath string, env map[string]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>` + Label + `</string>
    <key>ProgramArguments</key>
    <array>
        <string>` + xmlEscape(binPath) + `</string>
    </array>
`)
	if len(env) > 0 {
		b.WriteString("    <key>EnvironmentVariables</key>\n    <dict>\n")
		for _, k := range sortedKeys(env) {
			b.WriteString("        <key>" + xmlEscape(k) + "</key>\n")
			b.WriteString("        <string>" + xmlEscape(env[k]) + "</string>\n")
		}
		b.WriteString("    </dict>\n")
	}
	b.WriteString(`    <key>ProcessType</key>
    <string>Background</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/ts-autoserve.out</string>
    <key>StandardErrorPath</key>
    <string>/tmp/ts-autoserve.err</string>
</dict>
</plist>
`)
	return b.String()
}

func unit(binPath string, env map[string]string) string {
	var b strings.Builder
	b.WriteString(`[Unit]
Description=Publish local dev servers on the tailnet
Documentation=https://github.com/breakzplatform/ts-autoserve
After=tailscaled.service

[Service]
ExecStart=` + binPath + `
Restart=always
RestartSec=5
`)
	for _, k := range sortedKeys(env) {
		b.WriteString("Environment=\"" + k + "=" + env[k] + "\"\n")
	}
	b.WriteString(`
[Install]
WantedBy=default.target
`)
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Small maps; insertion sort keeps this dependency-free and stable.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s: %s", err, msg)
	}
	return nil
}

// State describes whether the service is installed and running.
type State struct {
	Installed bool
	Running   bool
	Path      string
	Detail    string // PID, "not loaded", or whatever the OS reports
}

// Status reports the current state of the user service.
func Status() (State, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return State{}, err
	}
	var st State
	switch runtime.GOOS {
	case "darwin":
		st.Path = filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
		st.Installed = exists(st.Path)
		out, err := exec.Command("launchctl", "list").Output()
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if !strings.HasSuffix(line, Label) {
					continue
				}
				f := strings.Fields(line)
				if len(f) >= 1 && f[0] != "-" {
					st.Running = true
					st.Detail = "pid " + f[0]
				} else {
					st.Detail = "loaded, not running"
				}
			}
		}
	case "linux":
		st.Path = filepath.Join(home, ".config", "systemd", "user", UnitName)
		st.Installed = exists(st.Path)
		out, _ := exec.Command("systemctl", "--user", "is-active", UnitName).Output()
		state := strings.TrimSpace(string(out))
		st.Running = state == "active"
		st.Detail = state
	default:
		return State{}, fmt.Errorf("service status is not supported on %s", runtime.GOOS)
	}
	if st.Detail == "" {
		st.Detail = "not loaded"
	}
	return st, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
