package vcs

import (
	"bytes"
	"context"
	"strings"
)

// CommitSubject returns the subject of the commit rev resolves to: the first
// line of the commit's message that is not blank, with the space around it
// trimmed. A message with no such line, including an empty one, has the empty
// subject, which is an answer rather than an error.
//
// The rule is this package's rather than any git formatting placeholder's: the
// raw commit object is read with cat-file, the message is everything after the
// object's first empty line, and the subject is the line described above. The
// tests build a commit whose header carries a field running over several
// lines, the shape a signed commit has, and find the subject in the message
// rather than in that field. The bytes are not re-encoded: a commit that
// records another encoding has its subject returned as stored, and whatever in
// it is not valid UTF-8 is replaced with U+FFFD, so the answer is always valid
// UTF-8 text.
//
// It returns ErrRefNotFound when rev names nothing or nothing that peels to a
// commit, and ErrMalformedOutput when the object has no empty line to end its
// header. It removes nothing from the subject: what the text may carry is the
// caller's to judge, and a caller that records it or sends it to a code host
// passes it through internal/redact first.
func (r *Repository) CommitSubject(ctx context.Context, rev string) (string, error) {
	commit, err := r.ResolveCommit(ctx, rev)
	if err != nil {
		return "", err
	}
	out, err := r.run(ctx, "commit-subject", "cat-file", "commit", commit)
	if err != nil {
		return "", err
	}
	end := bytes.Index(out, []byte("\n\n"))
	if end < 0 {
		return "", &outputError{op: "commit-subject", detail: "commit " + commit + " has no empty line ending its header"}
	}
	for _, line := range strings.Split(string(out[end+2:]), "\n") {
		if subject := strings.TrimSpace(line); subject != "" {
			return strings.ToValidUTF8(subject, "�"), nil
		}
	}
	return "", nil
}
