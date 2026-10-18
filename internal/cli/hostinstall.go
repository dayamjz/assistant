package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/machine"
)

// The host's settings: where they live and the names the line is built
// from. Claude Code is the default host; Codex uses hooks.json under
// CODEX_HOME instead, on hostHooksFile's terms. Every name here for Claude
// is one its own documentation states, as the 2.1.299 build carries it:
// settings cascade from "~/.claude/settings.json (user)", hooks
// are the "hooks" key, each event maps to a list of entries whose "hooks"
// list holds {"type": "command", "command": ...}, and the event that runs
// "right before Claude concludes its response" is "Stop", with "Exit code 2 -
// show stderr to model and continue conversation", which is hostBlockCode's
// convention. Its own messages name CLAUDE_CONFIG_DIR as where a person's
// own settings are found and HOME as what that defaults from, so a set
// CLAUDE_CONFIG_DIR is read as the directory standing in for ~/.claude.
// These are what the host says about itself, not something this package
// verified by running it. The line sets no re-entry signal of its own:
// the same host documents stop_hook_active on the hook's standard input,
// and hostStop reads it there; hostReentryField names the source.
const (
	hostConfigDirVar   = "CLAUDE_CONFIG_DIR"
	hostConfigDirName  = ".claude"
	hostSettingsName   = "settings.json"
	hostHooksKey       = "hooks"
	hostTurnEndEvent   = "Stop"
	hostCommandType    = "command"
	hostInstallWriteFl = "write"
)

// hostInstall is the installer PRD section 6's stop guard row asks for: it
// prints the exact line the host's configuration needs, and writes it only
// when given --write, so without that flag nothing in the host's
// configuration changes (P1). --host selects Claude Code or Codex; each
// writes only its own file and never the other's. A push never consented to
// a change in the host's configuration, and neither did running this command
// without the flag.
//
// The line's command is this binary's absolute path with the home this
// invocation resolved, so the hook judges the home it was installed for
// whatever environment the host gives it, and it cannot be redirected through
// PATH; it is the shape internal/gate gives the gate's hooks, for the same
// reasons.
//
// Under --write the line is merged into the settings file rather than
// written over it: every other key, and every other hook, is kept in its
// order, and the file is laid out afresh with two-space indentation, so the
// bytes of what was kept can change while its content does not. A file this
// cannot read as a settings document - one that does not parse, has a
// duplicated key on the path the merge walks, or has a value of another
// shape there - is refused rather than repaired, because a merge that
// guessed would be a change nobody consented to. A file already holding a
// turn-end hook that runs this program's stop guard with a different
// command line is refused as a conflict, and one holding exactly this line
// is reported present and not touched. Each refusal is a typed result that
// exits with machine.ExitFailure.
//
// Three gaps are this command's and are not closed here. The write is
// anchored to the bytes the merge read: those are read again just before the
// new file is renamed into place and a difference is a refusal, but a change
// that lands between that read and the rename is overwritten, and nothing
// here locks the file against the host or an editor. Telling this program's
// hook from another's is textual: a hook whose command line ends in "host
// stop" and whose program has this binary's file name is this program's,
// so one reached through a link or a wrapper is not recognised as a
// conflict. Last, the file is the one the host documents as a person's user settings
// rather than one tied to the coordinator's directory, which this command
// does not know, so the line is configured beside every session that file
// applies to and not only the coordinator's.
func hostInstall(_ context.Context, in *invocation) (any, error) {
	write := false
	hostName := string(HostClaude)
	if err := in.parseFlags("host install", func(set *flag.FlagSet) {
		set.BoolVar(&write, hostInstallWriteFl, false, "write the line into the host's settings file; without it nothing is written")
		set.StringVar(&hostName, hostInstallHostFl, string(HostClaude), "coordinator host to install for: "+hostKindNames())
	}); err != nil {
		return nil, err
	}
	kind, err := parseHostKind(hostName)
	if err != nil {
		return nil, usageError{err: err}
	}
	if err := in.resolveHome(); err != nil {
		return nil, err
	}
	settings, err := hostHooksFile(kind, in.env.Getenv)
	if err != nil {
		return nil, err
	}
	if in.env.Executable == "" || !filepath.IsAbs(in.env.Executable) {
		return nil, fmt.Errorf("the path of this binary is not known as an absolute path, so the hook could not name it")
	}
	command := shellQuote(in.env.Executable) + " --" + homeFlagName + " " + shellQuote(in.home.Root()) + " " + hostVerbName + " stop"
	entry, err := hostEntry(command)
	if err != nil {
		return nil, err
	}
	line, err := compactJSON(map[string]any{hostHooksKey: map[string]any{hostTurnEndEvent: []json.RawMessage{entry}}})
	if err != nil {
		return nil, err
	}
	answer := machine.HostInstall{
		Settings: settings,
		Line:     line,
		Command:  command,
		Host:     string(kind),
		Result:   machine.HostInstallPrinted,
	}
	if !write {
		return answer, nil
	}
	return mergeHostSettings(answer, entry, in.env.Executable), nil
}

// hostSettingsPath is the host's user settings file, from the environment
// this invocation was given rather than the process's.
func hostSettingsPath(getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if dir := getenv(hostConfigDirVar); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("%s is %q, which is not an absolute path, so the host's settings file could not be placed", hostConfigDirVar, dir)
		}
		return filepath.Join(dir, hostSettingsName), nil
	}
	userHome := getenv("HOME")
	if userHome == "" || !filepath.IsAbs(userHome) {
		return "", fmt.Errorf("HOME is %q, which is not an absolute path, so the host's settings file could not be placed", userHome)
	}
	return filepath.Join(userHome, hostConfigDirName, hostSettingsName), nil
}

// hostEntry is the one entry the line adds under the turn-end event.
func hostEntry(command string) (json.RawMessage, error) {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	text, err := compactJSON(struct {
		Hooks []hook `json:"hooks"`
	}{Hooks: []hook{{Type: hostCommandType, Command: command}}})
	return json.RawMessage(text), err
}

// compactJSON encodes v on one line without escaping the characters HTML
// cares about, so a path with an ampersand in it reads as the path.
func compactJSON(v any) (string, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// mergeHostSettings adds entry to the settings file answer names, under the
// explicit flag, and says what became of it. Every path through it either
// writes the merged file or leaves the file as it was.
func mergeHostSettings(answer machine.HostInstall, entry json.RawMessage, executable string) machine.HostInstall {
	refuse := func(refusal machine.HostInstallRefusal, detail string) machine.HostInstall {
		answer.Result, answer.Refusal, answer.Detail = machine.HostInstallRefused, refusal, detail
		return answer
	}
	observed, err := os.ReadFile(answer.Settings)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return refuse(machine.HostInstallUnreadable, err.Error())
	}
	mode := fs.FileMode(0o600)
	if exists {
		if info, err := os.Stat(answer.Settings); err == nil {
			mode = info.Mode().Perm()
		}
	}
	merged := []byte("{}")
	if exists {
		merged = observed
	}
	next, present, conflict, err := mergeHostEntry(merged, entry, answer.Command, executable)
	switch {
	case err != nil:
		return refuse(machine.HostInstallUnreadable, err.Error())
	case conflict != "":
		answer.Conflict = conflict
		return refuse(machine.HostInstallConflict, "a turn-end hook runs this program's stop guard with a different command line")
	case present:
		answer.Result = machine.HostInstallPresent
		return answer
	}
	var laid bytes.Buffer
	if err := json.Indent(&laid, next, "", "  "); err != nil {
		return refuse(machine.HostInstallUnreadable, err.Error())
	}
	laid.WriteByte('\n')
	if err := writeAnchored(answer.Settings, laid.Bytes(), mode, observed, exists); err != nil {
		var changed changedError
		if errors.As(err, &changed) {
			return refuse(machine.HostInstallChanged, err.Error())
		}
		return refuse(machine.HostInstallUnreadable, err.Error())
	}
	answer.Result = machine.HostInstallWritten
	return answer
}

// changedError is a settings file that no longer holds what the merge read.
type changedError struct{ path string }

// Error says which file changed.
func (e changedError) Error() string {
	return e.path + " changed after it was read, so the merge was not written over it"
}

// writeAnchored writes content to path through a file beside it that is
// renamed into place, after reading path again and finding it still holds
// observed, or still absent when it was. The directory is created when it is
// missing, readable by its owner only.
func writeAnchored(path string, content []byte, mode fs.FileMode, observed []byte, existed bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	staged, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	stagedPath := staged.Name()
	defer func() { _ = os.Remove(stagedPath) }()
	if _, err := staged.Write(content); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Chmod(mode); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Sync(); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	now, err := os.ReadFile(path)
	switch {
	case err == nil && (!existed || !bytes.Equal(now, observed)):
		return changedError{path: path}
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return err
	case err != nil && existed:
		return changedError{path: path}
	}
	return os.Rename(stagedPath, path)
}

// member is one key of a JSON object and its value as written, so an object
// is re-encoded with its keys in the order the file had them.
type member struct {
	key   string
	value json.RawMessage
}

// mergeHostEntry adds entry to the turn-end event's list in document, and
// reports instead that the list already holds exactly command, or the command
// line of a hook there that runs this program's stop guard differently. An
// error is a document the merge cannot read without guessing.
func mergeHostEntry(document, entry json.RawMessage, command, executable string) (merged json.RawMessage, present bool, conflict string, err error) {
	top, err := objectMembers(document)
	if err != nil {
		return nil, false, "", fmt.Errorf("the settings file is not a settings document: %w", err)
	}
	hooksAt := find(top, hostHooksKey)
	var hooks []member
	if hooksAt >= 0 {
		if hooks, err = objectMembers(top[hooksAt].value); err != nil {
			return nil, false, "", fmt.Errorf("%q is not an object of events: %w", hostHooksKey, err)
		}
	}
	eventAt := find(hooks, hostTurnEndEvent)
	var entries []json.RawMessage
	if eventAt >= 0 {
		if err := json.Unmarshal(hooks[eventAt].value, &entries); err != nil || entries == nil {
			return nil, false, "", fmt.Errorf("%q.%q is not a list of entries", hostHooksKey, hostTurnEndEvent)
		}
	}
	for _, existing := range entries {
		commands, err := entryCommands(existing)
		if err != nil {
			return nil, false, "", fmt.Errorf("an entry under %q.%q: %w", hostHooksKey, hostTurnEndEvent, err)
		}
		for _, c := range commands {
			switch {
			case c == command:
				present = true
			case runsThisStopGuard(c, executable):
				conflict = c
			}
		}
	}
	if conflict != "" || present {
		return nil, present, conflict, nil
	}
	list, err := encodeList(append(entries, entry))
	if err != nil {
		return nil, false, "", err
	}
	if eventAt >= 0 {
		hooks[eventAt].value = list
	} else {
		hooks = append(hooks, member{key: hostTurnEndEvent, value: list})
	}
	encodedHooks, err := encodeObject(hooks)
	if err != nil {
		return nil, false, "", err
	}
	if hooksAt >= 0 {
		top[hooksAt].value = encodedHooks
	} else {
		top = append(top, member{key: hostHooksKey, value: encodedHooks})
	}
	merged, err = encodeObject(top)
	return merged, false, "", err
}

// entryCommands is the command lines of an entry's command hooks. An entry
// that is not an object, or whose hooks are not a list of objects, is an
// error, because whether it runs this program cannot be told.
func entryCommands(entry json.RawMessage) ([]string, error) {
	fields, err := objectMembers(entry)
	if err != nil {
		return nil, err
	}
	at := find(fields, hostHooksKey)
	if at < 0 {
		return nil, nil
	}
	var hooks []json.RawMessage
	if err := json.Unmarshal(fields[at].value, &hooks); err != nil || hooks == nil {
		return nil, fmt.Errorf("%q is not a list of hooks", hostHooksKey)
	}
	var commands []string
	for _, raw := range hooks {
		hook, err := objectMembers(raw)
		if err != nil {
			return nil, err
		}
		var kind, command string
		if at := find(hook, "type"); at >= 0 {
			if err := json.Unmarshal(hook[at].value, &kind); err != nil {
				return nil, fmt.Errorf("a hook's type is not a string")
			}
		}
		if kind != hostCommandType {
			continue
		}
		if at := find(hook, "command"); at >= 0 {
			if err := json.Unmarshal(hook[at].value, &command); err != nil {
				return nil, fmt.Errorf("a command hook's command is not a string")
			}
		}
		commands = append(commands, command)
	}
	return commands, nil
}

// runsThisStopGuard reports whether a hook's command line runs this
// program's stop guard: its program has this binary's file name and its last
// two words are the host verb and its stop subcommand. It is textual and
// says nothing about a command reached through a link or a wrapper.
func runsThisStopGuard(command, executable string) bool {
	words, ok := shellWords(command)
	if !ok || len(words) < 3 {
		return false
	}
	return filepath.Base(words[0]) == filepath.Base(executable) &&
		words[len(words)-2] == hostVerbName && words[len(words)-1] == "stop"
}

// objectMembers reads a JSON object's members in order. Anything that is not
// one object, and an object naming a key twice, is an error: a duplicated
// key would leave which value the host reads to the host's parser.
func objectMembers(raw json.RawMessage) ([]member, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	open, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := open.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("not an object")
	}
	var members []member
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := token.(string)
		if seen[key] {
			return nil, fmt.Errorf("the key %q appears twice", key)
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		members = append(members, member{key: key, value: value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("there is more after the object")
	}
	return members, nil
}

// find is the index of key among members, or -1.
func find(members []member, key string) int {
	for i, m := range members {
		if m.key == key {
			return i
		}
	}
	return -1
}

// encodeObject writes members back as one object, in their order.
func encodeObject(members []member) (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// encodeList writes values back as one list, in their order.
func encodeList(values []json.RawMessage) (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(v)
	}
	b.WriteByte(']')
	return b.Bytes(), nil
}

// shellQuote quotes a word for a POSIX shell, leaving one that needs no
// quoting as it is so the common line reads plainly.
func shellQuote(word string) string {
	plain := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@%=,", r)
	}
	if word != "" && strings.IndexFunc(word, func(r rune) bool { return !plain(r) }) < 0 {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// shellWords splits a command line into words on a POSIX shell's quoting
// rules for single quotes, double quotes and backslashes, and nothing else:
// no expansion, no operators. It reports false for an unterminated quote.
func shellWords(line string) ([]string, bool) {
	var words []string
	var word strings.Builder
	inWord := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		case c == '\'':
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				return nil, false
			}
			word.WriteString(line[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case c == '"':
			i++
			for ; i < len(line) && line[i] != '"'; i++ {
				if line[i] == '\\' && i+1 < len(line) && strings.IndexByte(`"\$`+"`", line[i+1]) >= 0 {
					i++
				}
				word.WriteByte(line[i])
			}
			if i >= len(line) {
				return nil, false
			}
			inWord = true
		case c == '\\' && i+1 < len(line):
			i++
			word.WriteByte(line[i])
			inWord = true
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	return words, true
}
