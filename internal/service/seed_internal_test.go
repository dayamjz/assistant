package service

import (
	"testing"

	"github.com/dayamjz/assistant/internal/store/storetest"
)

// seedDatabase copies the database template internal/store/storetest owns to
// path when nothing is there yet, so the store.Open that follows finds a
// database at this build's schema. That package's comment says why it exists
// and what it does and does not change about what a test proves; creating a
// database from nothing is still exercised by every test here whose service
// opens a home nobody seeded.
func seedDatabase(tb testing.TB, path string) {
	tb.Helper()
	storetest.Seed(tb, path)
}

// removeDatabaseTemplate removes the template, for TestMain once every test
// has run.
func removeDatabaseTemplate() { storetest.Remove() }
