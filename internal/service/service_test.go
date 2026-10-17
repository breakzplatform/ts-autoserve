package service

import (
	"encoding/xml"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
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
