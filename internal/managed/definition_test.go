package managed

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// execStartWords reads the ExecStart line of a unit the way the stand-in
// manager does: every word is double-quoted, which systemdUnit guarantees by
// refusing the characters that would need escaping inside quotes.
func execStartWords(t *testing.T, unit string) []string {
	t.Helper()
	for _, line := range strings.Split(unit, "\n") {
		rest, ok := strings.CutPrefix(line, "ExecStart=")
		if !ok {
			continue
		}
		var words []string
		for {
			rest = strings.TrimLeft(rest, " ")
			if rest == "" {
				return words
			}
			if rest[0] != '"' {
				t.Fatalf("ExecStart holds an unquoted word: %q", line)
			}
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				t.Fatalf("ExecStart holds an unterminated word: %q", line)
			}
			words = append(words, rest[1:1+end])
			rest = rest[2+end:]
		}
	}
	t.Fatalf("the unit has no ExecStart:\n%s", unit)
	return nil
}

// programArguments reads a plist's ProgramArguments array with encoding/xml,
// as a property list reader would, so what is checked is what the escaping
// put on the wire rather than the text this package meant to write.
func programArguments(t *testing.T, plist []byte) []string {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(string(plist)))
	d.Strict = true
	var args []string
	inArray, wantArray := false, false
	var lastKey string
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the definition is not well-formed XML: %v", err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			switch tok.Name.Local {
			case "key":
				var k string
				if err := d.DecodeElement(&k, &tok); err != nil {
					t.Fatalf("reading a key: %v", err)
				}
				lastKey = k
				wantArray = k == "ProgramArguments"
			case "array":
				inArray = wantArray
			case "string":
				var v string
				if err := d.DecodeElement(&v, &tok); err != nil {
					t.Fatalf("reading a string: %v", err)
				}
				if inArray {
					args = append(args, v)
				}
			}
		case xml.EndElement:
			if tok.Name.Local == "array" {
				inArray, wantArray = false, false
			}
		}
	}
	if lastKey == "" {
		t.Fatal("the definition holds no keys")
	}
	return args
}

// A unit and a plist carry the program, the home and the foreground managed
// service exactly, read back the way each format is read: a home with a space
// in it survives the unit's quoting, and one with XML's own characters
// survives the plist's escaping.
func TestADefinitionStartsTheManagedServiceForItsHome(t *testing.T) {
	spec := Spec{Executable: "/opt/bin/assistant", Root: "/home/some one/.assistant", SearchPath: "/usr/bin:/bin"}
	want := append([]string{spec.Executable}, spec.Arguments()...)
	unit, err := systemdUnit(spec)
	if err != nil {
		t.Fatalf("systemdUnit: %v", err)
	}
	if got := execStartWords(t, string(unit)); !slices.Equal(got, want) {
		t.Fatalf("the unit starts %q, want %q", got, want)
	}
	for _, line := range []string{"Restart=on-failure", "KillMode=process", "WantedBy=default.target", `Environment="PATH=/usr/bin:/bin"`} {
		if !strings.Contains(string(unit), line+"\n") {
			t.Fatalf("the unit lacks %q:\n%s", line, unit)
		}
	}
	if !slices.Contains(want, "--managed") || !slices.Contains(want, "--foreground") {
		t.Fatalf("the started command line %q is not the foreground managed service", want)
	}

	spec.Root = "/Users/a&b/<home>"
	want = append([]string{spec.Executable}, spec.Arguments()...)
	plist, err := launchdPlist("assistant.service.x", spec)
	if err != nil {
		t.Fatalf("launchdPlist: %v", err)
	}
	if got := programArguments(t, plist); !slices.Equal(got, want) {
		t.Fatalf("the plist starts %q, want %q", got, want)
	}
}

// A value the unit format would read as something else is refused rather
// than escaped, and so is a relative path; the control is a plain path,
// accepted above.
func TestADefinitionRefusesAValueItWouldHaveToEscape(t *testing.T) {
	for _, bad := range []string{`/home/a"b`, `/home/a\b`, "/home/100%", "/home/$HOME", "/home/a\nb", "relative/home"} {
		_, err := systemdUnit(Spec{Executable: "/bin/assistant", Root: bad})
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Reason != ReasonUnwritable {
			t.Fatalf("systemdUnit with root %q = %v, want unwritable", bad, err)
		}
	}
	if _, err := launchdPlist("label", Spec{Executable: "/bin/assistant", Root: "/home/a\x01b"}); err == nil {
		t.Fatal("launchdPlist wrote a control character into XML")
	}
}

// The protected folders are the three under the user's home, compared
// ignoring case and through a symbolic link into one; a path beside them,
// and one whose name only begins like one, is not inside.
func TestProtectedPathsAreTheThreeFoldersAndWhatIsInThem(t *testing.T) {
	userHome := t.TempDir()
	documents := filepath.Join(userHome, "Documents")
	if err := os.MkdirAll(filepath.Join(documents, "code"), 0o755); err != nil {
		t.Fatal(err)
	}
	inside := []string{
		filepath.Join(userHome, "Documents", "code"),
		filepath.Join(userHome, "desktop", "x"),
		filepath.Join(userHome, "Downloads"),
	}
	// A link is part of the case where the platform lets a test make one,
	// which Windows may not without a privilege the test does not ask for.
	link := filepath.Join(userHome, "elsewhere")
	if err := os.Symlink(filepath.Join(documents, "code"), link); err == nil {
		inside = append(inside, link)
	} else {
		t.Logf("no symbolic link here, so the link case is not part of this run: %v", err)
	}
	outside := []string{
		filepath.Join(userHome, "src", "code"),
		filepath.Join(userHome, "DocumentsOld", "code"),
		filepath.Join(userHome, ".assistant"),
	}
	got := protectedPaths(userHome, append(slices.Clone(inside), outside...))
	if !slices.Equal(got, inside) {
		t.Fatalf("protectedPaths found %q, want exactly %q", got, inside)
	}
}
