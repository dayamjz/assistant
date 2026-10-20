---
paths: ["internal/fixture/**"]
---

# internal/fixture

- It runs git directly, the way `internal/vcs`'s own test helpers do, because
  a fixture built with the code under validation cannot show that code wrong.
- Two rules are load-bearing:
  - A condition is reached by the path the product takes to it, so the two
    states that only exist partway through a run are deferred to
    `AdvanceRemoteOutOfBand` and `CopyGatedWorkingCopy` rather than
    assembled.
  - "Nothing executed" is checked, not assumed: every planted executable
    appends to the scenario's tripwire file, and the package's own tests run
    one to prove the tripwire fires.
- Nothing here decides how a harness drives a condition; what it ran into is
  in `OpenQuestions`.

Build it with `scripts/build-fixture.sh DIR`, and read `doc.go` first.
