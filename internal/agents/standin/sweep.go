package standin

// RequireSweepClearVar names the environment variable that turns a host
// whose empty task copy cannot sweep clear from a skip into a failure, for
// the tests that assert a sweep clears. Such a host has a process outside
// every copy under test whose working directory the sweep cannot read, and
// agents.Sweeper refuses to clear while it runs, which is P6's refusal
// working; a test there can establish nothing about a clear sweep. CI sets
// the variable on the lane whose test step runs in a process table of its
// own, where no such process is visible, so a test there that would assert
// a clear sweep cannot read as passed by not having run. A lane that does
// not set it skips with the blockers named, and the workflow says which
// lane that is and why.
const RequireSweepClearVar = "ASSISTANT_REQUIRE_SWEEP_CLEAR"
