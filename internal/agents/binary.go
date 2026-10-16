package agents

import (
	"fmt"
	"os/exec"
	"strings"
)

// lookPathFirst returns the first name in names that resolves on PATH.
// An empty name is skipped. When none resolve, the error names every
// candidate that was tried, so a doctor or Resolution.Skipped entry can say
// what was missing rather than only the last LookPath failure.
func lookPathFirst(names ...string) (string, error) {
	var tried []string
	var last error
	for _, name := range names {
		if name == "" {
			continue
		}
		tried = append(tried, name)
		resolved, err := exec.LookPath(name)
		if err == nil {
			return resolved, nil
		}
		last = err
	}
	if len(tried) == 0 {
		return "", fmt.Errorf("no executable name to resolve")
	}
	if len(tried) == 1 {
		return "", fmt.Errorf("%s is not runnable: %w", tried[0], last)
	}
	return "", fmt.Errorf("none of %s is runnable: %w", strings.Join(tried, ", "), last)
}

// withoutEnvNames returns env with every NAME=… entry whose name is in
// drop removed. It is how an adapter keeps a CLI on its login credentials
// when the process environment carries a provider API key that would
// otherwise take over. Matching is exact on the name before '=', so a
// similarly prefixed variable is left alone.
func withoutEnvNames(env []string, drop ...string) []string {
	if len(env) == 0 || len(drop) == 0 {
		return append([]string(nil), env...)
	}
	deny := make(map[string]struct{}, len(drop))
	for _, name := range drop {
		if name != "" {
			deny[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, drop := deny[name]; drop {
			continue
		}
		out = append(out, entry)
	}
	return out
}
