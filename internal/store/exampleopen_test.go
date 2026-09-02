package store_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// homeDir stands in for the one root PRD section 8 puts everything under. The
// examples create a temporary one so they can run.
func homeDir() (string, error) {
	return os.MkdirTemp("", "assistant-example-")
}

// openForExample returns a store and the func that closes it and removes the
// temporary home it was opened in. An example has no *testing.T, so t.TempDir
// is not available and the cleanup is the caller's to defer.
func openForExample() (*store.Store, func()) {
	home, err := homeDir()
	if err != nil {
		panic(err)
	}
	userinfo := regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/@\s]+)@`)
	s, err := store.Open(context.Background(), filepath.Join(home, "state.db"),
		store.WithRedactor(vcs.RedactorFunc(func(s string) string {
			return userinfo.ReplaceAllString(s, "${1}REDACTED@")
		})))
	if err != nil {
		_ = os.RemoveAll(home)
		panic(err)
	}
	return s, func() {
		_ = s.Close()
		_ = os.RemoveAll(home)
	}
}
