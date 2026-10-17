package service

import (
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// plistTexts returns the text of every <key> and <string> in document order,
// after a real XML parser has decoded it: what launchd would read.
func plistTexts(t *testing.T, doc string) []string {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	var out []string
	var in string
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("plist is not well-formed XML: %v\n%s", err, doc)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			if tok.Name.Local == "key" || tok.Name.Local == "string" {
				in = tok.Name.Local
				text.Reset()
			}
		case xml.CharData:
			if in != "" {
				text.Write(tok)
			}
		case xml.EndElement:
			if in != "" && tok.Name.Local == in {
				out = append(out, text.String())
				in = ""
			}
		}
	}
	return out
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

func TestPlistWithoutEnv(t *testing.T) {
	doc := plist("/Users/dev/.local/bin/ts-autoserve", nil)
	texts := plistTexts(t, doc)
	if i := indexOf(texts, "Label"); i < 0 || texts[i+1] != Label {
		t.Errorf("Label missing or wrong: %q", texts)
	}
	if i := indexOf(texts, "ProgramArguments"); i < 0 || texts[i+1] != "/Users/dev/.local/bin/ts-autoserve" {
		t.Errorf("ProgramArguments missing or wrong: %q", texts)
	}
	if strings.Contains(doc, "EnvironmentVariables") {
		t.Error("an empty environment should leave out the EnvironmentVariables block")
	}
	for _, want := range []string{"<key>RunAtLoad</key>\n    <true/>", "<key>KeepAlive</key>\n    <true/>"} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestPlistEscapesWhatXMLWouldMisread(t *testing.T) {
	bin := `/Users/dev/R&D <tools>/ts-autoserve`
	env := map[string]string{
		"TELEGRAM_BOT_TOKEN": `123:abc&def<ghi>"jkl'`,
		"A_FIRST":            "plain",
	}
	doc := plist(bin, env)
	texts := plistTexts(t, doc)
	if i := indexOf(texts, "ProgramArguments"); i < 0 || texts[i+1] != bin {
		t.Errorf("binary path did not survive: %q", texts)
	}
	i := indexOf(texts, "EnvironmentVariables")
	if i < 0 {
		t.Fatalf("no EnvironmentVariables in %q", texts)
	}
	// Sorted by name, so the file is the same on every install.
	got := texts[i+1 : i+5]
	want := []string{"A_FIRST", "plain", "TELEGRAM_BOT_TOKEN", env["TELEGRAM_BOT_TOKEN"]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("environment = %q, want %q", got, want)
	}
}

// unquoteSystemd undoes systemdQuoted the way systemd reads a double-quoted
// value: C-style escapes first, then %% for a literal %.
func unquoteSystemd(t *testing.T, s string) string {
	t.Helper()
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			i++
			if i == len(s) {
				t.Fatalf("dangling backslash in %q", s)
			}
			b.WriteByte(s[i])
		case c == '"':
			t.Fatalf("unescaped quote in %q", s)
		case c == '%':
			if i+1 == len(s) || s[i+1] != '%' {
				t.Fatalf("unescaped %% in %q would be a specifier", s)
			}
			i++
			b.WriteByte('%')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func TestUnitPlainPathMatchesPackagingFile(t *testing.T) {
	got := unit("/home/dev/go/bin/ts-autoserve", nil)
	data, err := os.ReadFile("../../packaging/systemd/ts-autoserve.service")
	if err != nil {
		t.Fatal(err)
	}
	// The packaged file uses the %h specifier, which the installer never
	// writes: it knows the real path.
	data = []byte(strings.Replace(string(data), "%h/go/bin/", "/home/dev/go/bin/", 1))
	// The packaged unit carries a comment for people editing it by hand;
	// otherwise the two must not drift apart.
	var want []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "#") {
			want = append(want, line)
		}
	}
	if got != strings.Join(want, "\n") {
		t.Errorf("unit drifted from packaging/systemd:\n--- generated\n%s\n--- packaged\n%s", got, strings.Join(want, "\n"))
	}
}

func TestUnitQuotesPathAndEnvironment(t *testing.T) {
	env := map[string]string{
		"TELEGRAM_BOT_TOKEN": `12:a"b\c%d e$f`,
		"B":                  "",
	}
	doc := unit("/home/dev/my tools/50%/$HOME/ts-autoserve", env)

	var exec string
	var envLines []string
	for _, line := range strings.Split(doc, "\n") {
		if v, ok := strings.CutPrefix(line, "ExecStart="); ok {
			exec = v
		}
		if v, ok := strings.CutPrefix(line, "Environment="); ok {
			envLines = append(envLines, v)
		}
	}
	if exec != `"/home/dev/my tools/50%%/$$HOME/ts-autoserve"` {
		t.Errorf("ExecStart = %s", exec)
	}
	if len(envLines) != 2 {
		t.Fatalf("Environment lines = %q", envLines)
	}
	for i, k := range []string{"B", "TELEGRAM_BOT_TOKEN"} {
		line := envLines[i]
		if !strings.HasPrefix(line, `"`) || !strings.HasSuffix(line, `"`) {
			t.Fatalf("not quoted: %s", line)
		}
		got := unquoteSystemd(t, line[1:len(line)-1])
		if got != k+"="+env[k] {
			t.Errorf("Environment %d reads back as %q, want %q", i, got, k+"="+env[k])
		}
	}
}

func TestExecPath(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/local/bin/ts-autoserve":        "/usr/local/bin/ts-autoserve",
		"/home/dev/.local/bin/ts-autoserve":  "/home/dev/.local/bin/ts-autoserve",
		"/opt/ts-autoserve_v0.2.0/bin/x+y@z": "/opt/ts-autoserve_v0.2.0/bin/x+y@z",
		"/home/dev/a b/ts-autoserve":         `"/home/dev/a b/ts-autoserve"`,
		`/home/dev/a"b\c/ts-autoserve`:       `"/home/dev/a\"b\\c/ts-autoserve"`,
		"/home/dev/a;b":                      `"/home/dev/a;b"`,
	} {
		if got := execPath(in); got != want {
			t.Errorf("execPath(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := map[string]string{"TELEGRAM_BOT_TOKEN": "123:abc", "_X1": ""}
	if err := validate("/usr/local/bin/ts-autoserve", ok); err != nil {
		t.Errorf("valid input refused: %v", err)
	}
	for name, c := range map[string]struct {
		bin string
		env map[string]string
	}{
		"empty path":       {"", nil},
		"newline in path":  {"/bin/x\nExecStartPre=/bin/evil", nil},
		"newline in value": {"/bin/x", map[string]string{"TOKEN": "abc\nExecStartPre=/bin/evil"}},
		"tab in value":     {"/bin/x", map[string]string{"TOKEN": "a\tb"}},
		"NUL in value":     {"/bin/x", map[string]string{"TOKEN": "a\x00b"}},
		"name with =":      {"/bin/x", map[string]string{"A=B": "c"}},
		"name with space":  {"/bin/x", map[string]string{"A B": "c"}},
		"name with digit":  {"/bin/x", map[string]string{"1A": "c"}},
		"empty name":       {"/bin/x", map[string]string{"": "c"}},
	} {
		if err := validate(c.bin, c.env); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSortedKeys(t *testing.T) {
	got := sortedKeys(map[string]string{"c": "", "a": "", "b": "", "B": ""})
	if want := []string{"B", "a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sortedKeys = %v, want %v", got, want)
	}
}

// fakeOS records the commands the installer runs instead of running them.
type fakeOS struct {
	calls []string
	fail  map[string]int // command prefix -> how many times to fail it
}

func (f *fakeOS) run(name string, args ...string) error {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call)
	for prefix, n := range f.fail {
		if strings.HasPrefix(call, prefix) && n != 0 {
			f.fail[prefix] = n - 1
			return errors.New("failed: " + call)
		}
	}
	return nil
}

func withFakeOS(t *testing.T, f *fakeOS) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldRun, oldSleep := runCmd, sleep
	runCmd, sleep = f.run, func(time.Duration) {}
	t.Cleanup(func() { runCmd, sleep = oldRun, oldSleep })
	return home
}

func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestInstallSystemdWritesPrivateUnitAndStartsIt(t *testing.T) {
	f := &fakeOS{}
	home := withFakeOS(t, f)
	path := filepath.Join(home, ".config", "systemd", "user", UnitName)

	// A unit left from an earlier install with looser permissions must not
	// keep them once a token is written into it.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := installSystemd("/home/dev/.local/bin/ts-autoserve", map[string]string{"TELEGRAM_BOT_TOKEN": "123:abc"})
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("path = %s, want %s", got, path)
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("unit mode = %o, want 600", m)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"ExecStart=/home/dev/.local/bin/ts-autoserve\n", `Environment="TELEGRAM_BOT_TOKEN=123:abc"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("unit lacks %q:\n%s", want, data)
		}
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now " + UnitName,
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}

	f.calls = nil
	if _, err := uninstallSystemd(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("unit still there after uninstall: %v", err)
	}
	want = []string{
		"systemctl --user disable --now " + UnitName,
		"systemctl --user daemon-reload",
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("uninstall commands = %q, want %q", f.calls, want)
	}
	// Uninstalling twice is not an error.
	if _, err := uninstallSystemd(); err != nil {
		t.Errorf("second uninstall: %v", err)
	}
}

func TestInstallSystemdReportsAFailedStart(t *testing.T) {
	f := &fakeOS{fail: map[string]int{"systemctl --user enable": 1}}
	home := withFakeOS(t, f)
	path, err := installSystemd("/bin/ts-autoserve", nil)
	if err == nil || !strings.Contains(err.Error(), "systemctl enable") {
		t.Fatalf("err = %v, want the enable failure", err)
	}
	if path != filepath.Join(home, ".config", "systemd", "user", UnitName) {
		t.Errorf("path = %s: the caller should still learn where the unit is", path)
	}
}

func TestInstallLaunchdWritesPrivatePlistAndBootstraps(t *testing.T) {
	// launchctl print failing means the old job is gone; bootstrap fails
	// once, as it does while launchd is still tearing the old job down.
	f := &fakeOS{fail: map[string]int{"launchctl print": -1, "launchctl bootstrap": 1}}
	home := withFakeOS(t, f)

	path, err := installLaunchd("/Users/dev/.local/bin/ts-autoserve", map[string]string{"TELEGRAM_BOT_TOKEN": "a&b"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "Library", "LaunchAgents", Label+".plist"); path != want {
		t.Errorf("path = %s, want %s", path, want)
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("plist mode = %o, want 600", m)
	}
	data, _ := os.ReadFile(path)
	if texts := plistTexts(t, string(data)); indexOf(texts, "a&b") < 0 {
		t.Errorf("token did not survive: %q", texts)
	}

	target := "gui/" + strconv.Itoa(os.Getuid())
	want := []string{
		"launchctl bootout " + target + "/" + Label,
		"launchctl print " + target + "/" + Label,
		"launchctl bootstrap " + target + " " + path,
		"launchctl bootstrap " + target + " " + path,
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("commands = %q, want %q", f.calls, want)
	}

	if _, err := uninstallLaunchd(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("plist still there after uninstall: %v", err)
	}
}

func TestInstallLaunchdGivesUpAfterThreeTries(t *testing.T) {
	f := &fakeOS{fail: map[string]int{"launchctl print": -1, "launchctl bootstrap": -1}}
	withFakeOS(t, f)
	if _, err := installLaunchd("/bin/ts-autoserve", nil); err == nil || !strings.Contains(err.Error(), "launchctl bootstrap") {
		t.Fatalf("err = %v, want a bootstrap failure", err)
	}
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "launchctl bootstrap") {
			n++
		}
	}
	if n != 3 {
		t.Errorf("bootstrap tried %d times, want 3", n)
	}
}

func TestInstallRefusesBadInputBeforeWriting(t *testing.T) {
	f := &fakeOS{}
	home := withFakeOS(t, f)
	_, err := Install("/bin/ts-autoserve", map[string]string{"TOKEN": "a\nExecStartPre=/bin/evil"})
	if err == nil {
		t.Fatal("Install accepted a value with a line break")
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran: %q", f.calls)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Errorf("files written under HOME: %v", entries)
	}
}
