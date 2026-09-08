# The end-to-end journey harness

This package drives the `assistant` binary, as a process, against the
adversarial subject repository `internal/fixture` builds, and it is organized
around PRD section 4's principles rather than around the product's features. A
harness organized by feature covers the surface and proves none of the
guarantees; PRD section 13 already turns each principle into a test, and that
is the structure here.

## The limit that matters most: green here is not evidence the gate catches bugs

**A scripted agent proves the machinery, not the review quality.** Every
finding this harness sees is one this repository wrote and planted, and the
stand-in agent answers exactly what a test scripted it to answer. Nothing here
asks whether a real reviewing agent would have found a real defect, or whether
a real fix would have been correct.

Whether the gate catches real bugs needs a corpus of real changes with known
verdicts, judged against those verdicts. The PRD defers that, and this harness
does not stand in for it. A green run of this package says the mechanisms
behave as specified on inputs we chose. It says nothing about review quality,
and no report built on this may claim otherwise.

## The second limit: eight of the nine stages have no body

`internal/stages` has an implementation for the intent stage and none for the
other eight, and that one reads the intent it was supplied and launches
nothing. Every stage without a body reports one unclassified finding and holds
for a person, which is P3 working as specified and is also the reason large
parts of the product are not reachable from a run today:

- no stage launches an agent, so no run parses agent output, keeps a fixer
  session, or takes a fix round;
- no stage pushes, opens a pull request, or reads checks, so `internal/safety`,
  `internal/forge` and the push half of `internal/vcs` are never reached by a
  run;
- nothing reads a repository's own configuration document from anywhere, which
  `internal/service` states, so the trusted-versus-pushed composition PRD
  section 10 describes has no owner and no run performs it.

Where a mechanism cannot be reached through the binary, this harness drives it
through the package that owns it, against the same fixture, and says so. Every
row of `Coverage` and of `Drives` carries that distinction as `binary` or
`package` reach, so a package-reach pass can never be read as an end-to-end
one.

## Every assertion is watched failing, on every run

A harness of checks that cannot fail reports green while proving nothing, which
is the exact false confidence this product exists to prevent, built into the
thing meant to verify it. So falsifiability is structural here rather than
something somebody once confirmed by hand:

- a principle check is a `Check[O]`: a list of `Clause[O]` over a typed
  observation of what the product actually did, plus the **counterfeit**
  observations it must reject;
- a counterfeit is a *mutation of the real observation*, never an observation
  written from nothing, so it starts from something the product did produce.
  What `Verify` enforces is that and nothing more: `Break` is an unconstrained
  `func(O) O`, so keeping the mutation inside a shape the product could still
  have produced is the writer's discipline rather than a checked property. The
  discipline holds - a counterfeit that states an impossible shape shows a
  clause failing against a failure the mechanism cannot reach, which is the
  dead guard `internal/agents/standin` exists against - and the residual gap is
  that nothing here can tell one from a faithful mutation;
- `Verify` runs both halves on every invocation. A clause that stopped
  discriminating fails where it is used, immediately, with a message naming the
  counterfeit it accepted;
- a check declaring no counterfeit is refused rather than run, **and so is one
  carrying a clause no counterfeit reaches**. The predicate is a list rather
  than one function for exactly that reason: answered as a whole, a check hides
  the part of itself that discriminates nothing, because its other assertions
  reject every counterfeit anyway. Three clauses here survived that way and
  were found in review;
- a clause asserting an **absence** - nothing fired, nothing was rejected, no
  run started, no session was carried - says so and carries `Possible`, which
  reports from the real observation whether the thing could have been there at
  all. A counterfeit mutates the model and cannot answer that, so an absence
  clause that is falsifiable in the model and vacuous in the subject is the one
  shape counterfeits alone never catch. A clause that is not an absence may not
  carry a precondition, so the writer has to decide which kind it is;
- a check's `What` **names its subject and may not be its claim**. It is bounded
  at 72 characters, and the sentence a reader sees is `Claim()`, composed from
  the clauses rather than written beside them. A free-text claim next to a list
  of assertions is two statements of the same thing that drift, and the prose is
  the one nothing checks; several checks here promised more than they asserted
  that way. The bound removes room for a paragraph of promises. It does **not**
  make a short claim impossible - a subject that lies in eight words is still
  writable, and only review catches that;
- `TestEveryCheckHereRefusesACheckThatCannotFail` drives `Verify` itself over
  checks that are defective in each of those ways, because a `Verify` that
  quietly accepted anything would make every check here green.

This is not a substitute for a person watching a test fail; it is the durable
form of it. It establishes that every assertion discriminates. That the
observation is real is established separately, by producing it from a real
process against a real repository.

## How the subject is read back

This package builds git command lines and runs them, which makes it the second
documented exception to the rule that `internal/vcs` is the only package
invoking git; `internal/fixture` is the first. Both take it for the same
reason: a harness that confirmed the product's own git operation by asking the
package under validation would be reporting that package agreeing with itself.
The rule is not weakened by either - it has one owner and two named exceptions,
and a third is a finding to raise rather than a precedent.

## Running it

```
make check                       # what CI runs; this package runs with everything else
go test ./internal/journey/      # this package alone
```

The binary under test is built from this module by default. To drive a shipped
artifact instead, name it:

```
ASSISTANT_BINARY=/path/to/assistant go test ./internal/journey/
```

The fixture is built from nothing on every run, so no git object graph is
checked in. That build costs a few seconds once per test binary. A test that
mutates a scenario takes it with `Claim`; a test that only needs a repository
to point a run at clones one, so scenarios are never shared by accident.

## Known limits of the binary this harness drives

These are filed and not yet fixed. They are not harness bugs, and nothing here
works around them.

| Limit | What this harness does |
| --- | --- |
| `internal/ipc` reads no local socket peer credentials outside linux and darwin, and identification is authority there, so every method that starts, answers, cancels or reruns a run is refused on any other platform. `internal/ipc/peer_unsupported.go` states it, and the `internal/cli` and `internal/service` tests that drive a run already skip for it. | Skips every check that drives a run, rather than passing it. A green run of this package on such a platform establishes exactly the checks that drive no run, and nothing about the ones it skipped; the enumeration is deliberately not repeated here, because a list of which is which rots against the tests and this file is not its owner. The `Coverage` note of every principle whose binary reach passes through a run says which half of that principle is left, and a `-v` run names every check that skipped. A skipped check that reads as a pass is the same defect as a clause that cannot fail, so this row is the one place a reader has to be able to find that. |
| A service may not come up outside linux and darwin at all. `internal/service` and `internal/cli` both carry a skip for exactly that, bounded by the same written-down platform predicate, and none of the three establishes that the socket really fails to bind there - Go offers a local socket on more platforms than `internal/ipc` can identify a peer on, so the guards are written to skip on a service that did not come up rather than on the assumption that none does. | Skips a check whose service did not come up, rather than failing for the platform, so a green run there establishes exactly the checks whose service did come up or which need none. Which those are is not listed here, for the reason the row above gives: a list of them rots the next time a check is added, and this file does not own it. The `Coverage` note of every principle a skip takes down says so, and a `-v` run names each check that skipped. Two helpers start every service these tests start and the guard is taken in one of them, because only that one gets an error back: a service asked for over the command surface answers as a document, so a configuration it refused and a socket it could not bind read alike and a guard there would skip the refusal the check exists to observe. That path's caller carries the skip instead. Nothing enforces that no test starts a service another way, and the helpers' own comments say so and name it a rule rather than a mechanism. |
| `assistant --version --json` writes a plain line where `assistant --json --version` writes a document. `internal/cli/doc.go` records it and the parser rework owns it. | Writes `--json` before the verb everywhere, which every verb honours, and reports the `--version` ordering it observed. |
| PRD section 9's table names no command for the two subcommands the gate's admission hook invokes, which `internal/gate/hooks.go` requires of the command surface. The surface carries them, so a push to a gate is admitted and starts a run; what is unreconciled is the specification, not the code. | Drives an admitted push and holds the run it authorized to the branch and the commit that were pushed, which is P1's positive half. A separate check holds a push made with no service up to the weaker property that the gate did not accept it with nothing checking it. |
| `core.hooksPath` in a git configuration file redirects a gate's own hooks. `internal/gate/doc.go` names it as an open gap. | Drives a push under the redirect and reports which hook ran, off the fixture's tripwire file. Reported as a known gap, never as a pass. |
| Nothing reads a repository's configuration document from the default branch, so PRD section 10's abort before launch has no owner. | Drives `config.Parse` and `vcs.Repository.FileAt` against the planted documents directly, and observes on a run that it starts anyway. Reported as a gap against section 10. That nothing a branch names is executed is established nowhere in this build; the row below says why. |
| Two of `internal/graph`'s three loop bounds sit on the back edge into a fixer, and no stage here can produce a fix-eligible finding: a stage without a body reports an ask finding, which never enters a fix round, and the one stage with a body declares no fix rounds and reports only notes. | Drives the run-wide step budget, which is reachable, and says the other two are not. |
| No shipped surface reports which agent a run resolved. No `internal/machine` shape carries one, and `doctor`'s `agent` check resolves the constant `auto` against the default catalog, so it answers what is runnable on this machine rather than what any run resolved; `internal/cli` says so itself. | Does not claim it. The P7 branch test establishes the pushed-configuration rejections and the suppression refusal instead, both of which observe something. |
| `internal/fixture`'s nothing-executed evidence has no producer for the branch-installation family. Every executable those conditions plant - the `.claude` hooks, the branch's agent binary, its `commands.test`, the `.githooks` scripts, `.envrc` - is reached only through a stage body that launches something, and the one body `internal/stages` has reads the supplied intent, so a run launches no agent, runs no configured command, and makes no commit or push. | Rests no clause on the tripwire file: one asserting that absence would hold whatever the product resolved. The P7 branch test reads it and logs what it holds, so the evidence is in place the day a stage body lands, and the `Drives` row for that condition says nothing about it is established. |
| It has no producer for the hostile-template family either, for a different reason. Those hooks are receive-side, so only a push to the gate could run them, and the four subtests that plant them make no push: each initializes a gate and reads how that came out, and none starts a service. So neither a promoted template `pre-receive` nor `update` nor `post-update` is reached. A push to a gate is admitted in this build, which the PRD section 9 row above records, so what leaves these four short is what they do rather than a door that is shut. | Rests no clause on the tripwire file, for the same reason as the row above, and none on the gate's hooks directory either - so whether a hook arrived is unestablished as well as whether one ran. What those four subtests establish is how the initialization came out: the two refusals refuse with the substrings their conditions record, and the two closed channels are not refused. |

## What accounts for what

Three tables, each checked in both directions so a row cannot outlive the thing
it describes:

- `Coverage()` - one row per PRD principle, saying whether this harness reaches
  it and at what reach. `TestEveryPrincipleIsDrivenHereOrDeclaredNotToBe`
  fails when a row and the `principles.Cite` calls in this package disagree.
- `Drives()` - one row per condition `internal/fixture` plants, naming the test
  that drives it or saying why nothing does.
  `TestEveryPlantedConditionIsDrivenOrDeclaredUndriven` fails when a planted
  condition has no row, a row names a condition that is not planted, or a row
  names a test this package does not have.
- `Settlements()` - this harness's answer to each of the eight decisions
  `internal/fixture` recorded and declined to make.
  `TestEveryQuestionTheFixtureLeftOpenIsSettledHere` fails when a question has
  no settlement or a settlement has no question.

None of them says a named test checks what its row claims. A row is a claim on
the same terms `internal/principles` states for a citation, and nothing here
can check it.
