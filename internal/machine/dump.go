package machine

import "github.com/dayamjz/assistant/internal/store"

// Dump is the answer to PRD section 9's dump: every record the taskstate
// resolver reads, plus wakes and kept work, as one evidence document (P13).
// It is a window onto the store, not a substitute for it. Live inputs the
// resolver also asks are not records and are not here.
type Dump = store.Dump
