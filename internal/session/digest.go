package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Normalize is the normalization Digest hashes, over the output stream and
// not a screen. The rules are derived from what the stand-in's worker mode
// emits, newline-terminated lines of plain text on both streams, and from
// the one redraw a line-oriented process can put on a pipe:
//
//   - the text is read as lines separated by "\n";
//   - within a line, only what follows the last "\r" is kept, which is what
//     a carriage return leaves visible when a process redraws the line it is
//     on;
//   - trailing spaces and tabs are removed from each line;
//   - a final "\n" is not significant, so a stream read before and after
//     its last newline arrived digests the same.
//
// Deliberately not normalized, each because nothing in this repository has
// been shown to produce it and a rule for it would be designed against a
// guess about the real adapter's output: escape sequences, which pass
// through as the bytes they are; leading whitespace; blank lines; the order
// of lines; and anything inside a line's text such as a counter or a time.
// A spinner drawn with escape sequences rather than a carriage return
// therefore reads as changed, which is the honest answer until the format
// is known.
func Normalize(text string) string {
	lines := strings.Split(text, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	for i, line := range lines {
		if at := strings.LastIndexByte(line, '\r'); at >= 0 {
			line = line[at+1:]
		}
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

// Digest is the hex SHA-256 of Normalize(text). Two captures digest equal
// exactly when their normalized text is equal; it is a comparison key and
// carries no verdict about progress, which is the supervisor's to draw.
func Digest(text string) string {
	sum := sha256.Sum256([]byte(Normalize(text)))
	return hex.EncodeToString(sum[:])
}
