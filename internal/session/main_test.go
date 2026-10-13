package session_test

import (
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
)

func TestMain(m *testing.M) {
	standin.Main()
	os.Exit(m.Run())
}
