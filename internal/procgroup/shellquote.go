package procgroup

import "strings"

// ShellQuote wraps s in single quotes for a POSIX shell, so the shell that
// runs a program this package wrote passes s to exec byte for byte: every
// embedded single quote is closed, escaped, and reopened as '\”. An empty
// string becomes ” .
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
