package cli_test

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/cli"
)

// The stand-in service manager. A managed service test puts a script named
// systemctl and one named launchctl on the search path it hands the surface;
// each runs this binary with standInManagerDirVar naming where to record the
// call and standInManagerUnitsVar naming the folder definitions are written
// in. It answers like the manager in one respect that matters to the
// surface - it starts the service a definition says, when it is asked to
// load or start one - and in no other: it restarts nothing on a failure exit
// and stops nothing when it unloads a definition, which is the surface's job
// to have done first. What a definition says is read from the definition the
// surface wrote, the way the manager reads it, so a definition starting the
// wrong command line starts nothing that answers.

const (
	standInManagerDirVar   = "ASSISTANT_TEST_MANAGER_DIR"
	standInManagerUnitsVar = "ASSISTANT_TEST_MANAGER_UNITS"
)

// standInManagerMain is what TestMain hands the process to when
// standInManagerDirVar names a directory, and it returns otherwise.
func standInManagerMain() {
	dir := os.Getenv(standInManagerDirVar)
	if dir == "" {
		return
	}
	os.Exit(serveStandInManager(dir, os.Getenv(standInManagerUnitsVar), os.Args[1:]))
}

// serveStandInManager records the call and starts what it was asked to.
func serveStandInManager(dir, units string, args []string) int {
	calls, err := os.OpenFile(filepath.Join(dir, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, _ = fmt.Fprintln(calls, strings.Join(args, " "))
	_ = calls.Close()

	var command []string
	switch {
	case len(args) == 4 && args[0] == "--user" && args[1] == "enable" && args[2] == "--now",
		len(args) == 3 && args[0] == "--user" && args[1] == "start":
		command, err = execStart(filepath.Join(units, args[len(args)-1]))
	case len(args) == 3 && args[0] == "bootstrap":
		command, err = programArguments(args[2])
	case len(args) == 2 && args[0] == "kickstart":
		label := args[1][strings.LastIndex(args[1], "/")+1:]
		command, err = programArguments(filepath.Join(units, label+".plist"))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "stand-in manager:", err)
		return 1
	}
	if len(command) == 0 {
		return 0
	}
	out, err := os.OpenFile(filepath.Join(dir, "service.out"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = out.Close() }()
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdout, cmd.Stderr = out, out
	// The service is this binary too, and must not take itself for the
	// stand-in manager.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, standInManagerDirVar+"=") && !strings.HasPrefix(kv, standInManagerUnitsVar+"=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cli.Detach(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "stand-in manager:", err)
		return 1
	}
	_ = cmd.Process.Release()
	return 0
}

// execStart is the command line a unit's ExecStart starts, read as every word
// double-quoted, which internal/managed writes and the unit format reads.
func execStart(unit string) ([]string, error) {
	raw, err := os.ReadFile(unit)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(line, "ExecStart=")
		if !ok {
			continue
		}
		var words []string
		for rest = strings.TrimSpace(rest); rest != ""; rest = strings.TrimLeft(rest, " ") {
			if rest[0] != '"' {
				return nil, fmt.Errorf("an unquoted word in %q", line)
			}
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				return nil, fmt.Errorf("an unterminated word in %q", line)
			}
			words = append(words, rest[1:1+end])
			rest = rest[2+end:]
		}
		return words, nil
	}
	return nil, errors.New("no ExecStart in " + unit)
}

// programArguments is a plist's ProgramArguments, read with encoding/xml as a
// property list reader reads it.
func programArguments(plist string) ([]string, error) {
	raw, err := os.ReadFile(plist)
	if err != nil {
		return nil, err
	}
	d := xml.NewDecoder(strings.NewReader(string(raw)))
	var args []string
	want, in := false, false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return args, nil
		}
		if err != nil {
			return nil, err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			switch tok.Name.Local {
			case "key":
				var k string
				if err := d.DecodeElement(&k, &tok); err != nil {
					return nil, err
				}
				want = k == "ProgramArguments"
			case "array":
				in = want
			case "string":
				var v string
				if err := d.DecodeElement(&v, &tok); err != nil {
					return nil, err
				}
				if in {
					args = append(args, v)
				}
			}
		case xml.EndElement:
			if tok.Name.Local == "array" {
				in, want = false, false
			}
		}
	}
}
