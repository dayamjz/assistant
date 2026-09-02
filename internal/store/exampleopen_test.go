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

func openForExample() *store.Store {
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
		panic(err)
	}
	return s
}
