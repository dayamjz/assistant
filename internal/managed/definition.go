package managed

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// systemdUnit is the user unit for spec.
//
// Every value is written inside double quotes, and a value holding a double
// quote, a backslash, a percent sign, a dollar sign or a control character is
// refused with ReasonUnwritable rather than escaped: each is a character the
// unit file format gives a meaning of its own inside a quoted value, and
// refusing is what keeps this from depending on the escaping rules being
// read right. A path with a space in it is written as it is.
//
// What the unit asks of systemd, as the directives systemd documents them and
// not as a claim about how systemd carries them out:
//
//   - ExecStart is this program serving the home in the foreground with
//     --managed, which is what has a requested restart end the process for
//     the manager to replace rather than launch a successor itself.
//   - Restart=on-failure with RestartSec=1 brings the service back after a
//     failure exit, which is how a managed restart ends, and leaves it down
//     after the success exit a requested stop ends with.
//   - KillMode=process stops only the service's own process when the unit
//     stops, so what the service started and left to outlive it - a worker's
//     terminal among them - is not ended with the unit, as it is not ended
//     when a service this program launched itself stops.
//   - WantedBy=default.target is what enable attaches the unit to, so it is
//     started when the user's systemd instance is: at login, or at boot for a
//     user with lingering enabled, which nothing here turns on.
//
// The service's standard output and error are left to the user's journal.
func systemdUnit(spec Spec) ([]byte, error) {
	if err := writable(spec, systemdUnwritable); err != nil {
		return nil, err
	}
	words := append([]string{spec.Executable}, spec.Arguments()...)
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = `"` + w + `"`
	}
	var b strings.Builder
	b.WriteString("# Written by assistant service start --install, and removed by assistant service stop --uninstall.\n")
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=assistant service for %s\n", spec.Root)
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(quoted, " "))
	if spec.SearchPath != "" {
		fmt.Fprintf(&b, "Environment=\"PATH=%s\"\n", spec.SearchPath)
	}
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=1\n")
	b.WriteString("KillMode=process\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return []byte(b.String()), nil
}

// launchdPlist is the launchd agent definition for spec under label.
//
// Values are written as XML text, escaped by encoding/xml, so the only ones
// refused with ReasonUnwritable are those holding a control character, which
// XML 1.0 does not carry.
//
// What the definition asks of launchd, as the keys launchd documents and not
// as a claim about how launchd carries them out:
//
//   - ProgramArguments is this program serving the home in the foreground
//     with --managed, as for systemd.
//   - RunAtLoad starts it when the job is loaded: at install, and at each
//     login, when launchd loads the user's LaunchAgents.
//   - KeepAlive with SuccessfulExit false asks launchd to start it again after
//     a failure exit, and not after the success exit a requested stop ends
//     with. ThrottleInterval asks for the least time between starts that the
//     key takes, so a requested restart is not held for launchd's default.
//   - AbandonProcessGroup asks launchd not to end the service's process group
//     with it, for the reason KillMode=process is in the systemd unit.
//   - StandardOutPath and StandardErrorPath are spec.Output.
//   - EnvironmentVariables carries PATH and nothing else.
func launchdPlist(label string, spec Spec) ([]byte, error) {
	if err := writable(spec, xmlUnwritable); err != nil {
		return nil, err
	}
	if strings.ContainsFunc(label, xmlUnwritable) {
		return nil, &Refusal{Reason: ReasonUnwritable, Detail: "the label"}
	}
	var b bytes.Buffer
	text := func(s string) {
		_ = xml.EscapeText(&b, []byte(s))
	}
	str := func(s string) {
		b.WriteString("<string>")
		text(s)
		b.WriteString("</string>")
	}
	key := func(k string) {
		b.WriteString("\t<key>")
		text(k)
		b.WriteString("</key>\n")
	}
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	key("Label")
	b.WriteString("\t")
	str(label)
	b.WriteString("\n")
	key("ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, w := range append([]string{spec.Executable}, spec.Arguments()...) {
		b.WriteString("\t\t")
		str(w)
		b.WriteString("\n")
	}
	b.WriteString("\t</array>\n")
	if spec.SearchPath != "" {
		key("EnvironmentVariables")
		b.WriteString("\t<dict>\n\t\t<key>PATH</key>")
		str(spec.SearchPath)
		b.WriteString("\n\t</dict>\n")
	}
	key("RunAtLoad")
	b.WriteString("\t<true/>\n")
	key("KeepAlive")
	b.WriteString("\t<dict>\n\t\t<key>SuccessfulExit</key><false/>\n\t</dict>\n")
	key("ThrottleInterval")
	b.WriteString("\t<integer>1</integer>\n")
	key("AbandonProcessGroup")
	b.WriteString("\t<true/>\n")
	if spec.Output != "" {
		key("StandardOutPath")
		b.WriteString("\t")
		str(spec.Output)
		b.WriteString("\n")
		key("StandardErrorPath")
		b.WriteString("\t")
		str(spec.Output)
		b.WriteString("\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes(), nil
}

// writable refuses a spec whose program, root, output or search path holds a
// character bad reports.
func writable(spec Spec, bad func(rune) bool) error {
	for _, v := range []struct{ what, value string }{
		{"the program's path", spec.Executable},
		{"the home root", spec.Root},
		{"the output file", spec.Output},
		{"the search path", spec.SearchPath},
	} {
		if strings.ContainsFunc(v.value, bad) {
			return &Refusal{Reason: ReasonUnwritable, Detail: v.what}
		}
	}
	if !filepath.IsAbs(spec.Executable) || !filepath.IsAbs(spec.Root) {
		return &Refusal{Reason: ReasonUnwritable, Detail: "the program's path and the home root must be absolute"}
	}
	return nil
}

// systemdUnwritable reports a character systemdUnit refuses.
func systemdUnwritable(r rune) bool {
	return r == '"' || r == '\\' || r == '%' || r == '$' || unicode.IsControl(r)
}

// xmlUnwritable reports a character launchdPlist refuses.
func xmlUnwritable(r rune) bool { return unicode.IsControl(r) }

// protectedFolders are the folders under a user's home that the launchd
// install holds paths to: Documents, Desktop and Downloads, which issue #234
// names and where no-mistakes' launchd daemon could not read repositories.
// macOS protects more locations than these - iCloud Drive and removable and
// network volumes among them, as Apple documents - and nothing here checks
// those.
func protectedFolders(userHome string) []string {
	return []string{
		filepath.Join(userHome, "Documents"),
		filepath.Join(userHome, "Desktop"),
		filepath.Join(userHome, "Downloads"),
	}
}

// protectedPaths is every path in paths that is one of protectedFolders or
// inside one, in the order given.
//
// The comparison ignores letter case, because the file system macOS installs
// by default does, so a path that differs from a protected folder only in
// case is treated as inside it: that refuses more than an exact comparison
// would, which is the direction a wrong answer may go in. A path is compared
// as given and again with symbolic links resolved where they resolve, so a
// link from elsewhere into a protected folder is caught; a path that does not
// exist is compared as given.
func protectedPaths(userHome string, paths []string) []string {
	var found []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		candidates := []string{filepath.Clean(p)}
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			candidates = append(candidates, resolved)
		}
		for _, c := range candidates {
			if inProtected(userHome, c) {
				found = append(found, p)
				break
			}
		}
	}
	return found
}

// inProtected reports whether path is a protected folder or inside one.
func inProtected(userHome, path string) bool {
	homes := []string{filepath.Clean(userHome)}
	if resolved, err := filepath.EvalSymlinks(userHome); err == nil {
		homes = append(homes, resolved)
	}
	for _, h := range homes {
		for _, folder := range protectedFolders(h) {
			lowerFolder, lowerPath := strings.ToLower(folder), strings.ToLower(path)
			if lowerPath == lowerFolder || strings.HasPrefix(lowerPath, lowerFolder+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}
