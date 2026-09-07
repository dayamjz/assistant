// Package journey is the end-to-end harness: it drives the assistant binary,
// as a process, against the adversarial subject internal/fixture builds, and
// it is organized around PRD section 4's principles rather than around this
// product's features.
//
// The organizing choice is the load-bearing one. A harness arranged by feature
// covers the surface and establishes none of the guarantees; PRD section 13
// already turns each principle into a test, and that is the shape here. One
// test per principle, named for what the principle requires, with the
// fixture's own recorded expectations as the answers.
//
// README.md in this directory is what a person reads. It carries the limits of
// what a green run means, which belong somewhere nobody can miss them, and the
// known limits of the binary being driven. This comment is the contract.
//
// # Green here is not evidence that the gate catches bugs
//
// A scripted agent proves the machinery, not the review quality. Every finding
// this harness sees is one this repository planted, and the stand-in answers
// what a test scripted it to answer. Whether a real reviewing agent would find
// a real defect needs a corpus of real changes with known verdicts, which the
// PRD defers and this harness does not stand in for. Nothing built on a green
// run of this package may say otherwise.
//
// # Reach is part of every claim
//
// internal/stages holds no stage body, so a run holds at every stage and large
// parts of this product are unreachable from one: no agent is launched, no
// reference is moved, no code host is asked anything, and no repository
// configuration is read. Where a mechanism cannot be reached through the
// binary, this harness drives it through the package that owns it against the
// same fixture, and every row of Coverage and Drives carries which of the two
// it was. A package-reach pass says nothing about what ships, and recording
// the difference is what stops it being read as though it did.
//
// # Every assertion carries the evidence that it can fail
//
// A harness of checks that cannot fail reports green while proving nothing,
// which is the false confidence this product exists to prevent built into the
// thing meant to verify it. Check, Clause and Counterfeit are the answer, and
// they are structural rather than a habit: a check is a list of clauses over a
// typed observation together with the counterfeit observations it must reject,
// and Verify runs both halves every time. A counterfeit is a mutation of the
// real observation rather than one written from nothing, so it cannot state a
// shape the product could not produce. A check declaring no counterfeit is
// refused.
//
// The predicate is a list rather than one function because a check answered as
// a whole hides the part of itself that discriminates nothing: the other
// assertions reject every counterfeit, so the dead one is invisible, and this
// package shipped three of them. Verify therefore holds every clause to the
// standard the check is held to, and refuses a clause no counterfeit reaches.
//
// One shape needs more than that. A clause asserting an absence - nothing
// fired, nothing was rejected, no run started, no session was carried - can be
// perfectly falsifiable in the model and vacuous in the subject, because a
// counterfeit mutates the model and says nothing about whether the thing could
// have been there at all. Such a clause declares itself an absence and carries
// Possible, which answers that from the real observation; a clause that is not
// an absence may not carry one.
//
// What all of that establishes is that every assertion discriminates. That the
// observation is real is established separately, by producing it from a real
// process against a real repository, which is what everything else in this
// package is for.
//
// # What is driven, and what is accounted for
//
// Three tables, each checked against its owner in both directions, so a row
// cannot outlive what it describes. Coverage says what this harness
// establishes about each principle and is checked against the principles its
// own tests cite. Drives says what it does about each condition
// internal/fixture plants and is checked against the built catalog. Settlements
// answers each decision internal/fixture recorded and declined to make, and is
// checked against the questions the catalog carries.
//
// None of them says a named test checks what its row claims. A row is a claim
// on the same terms internal/principles states for a citation: it says a test
// says it drives this, never that the test is enough.
//
// # The subject, and who owns which part of it
//
// internal/fixture builds the subject and records what each planted condition
// must produce, down to the substrings a message has to carry. It decides
// nothing about how a condition is reached, which is this package's to settle,
// and Settlements is where those decisions live rather than in an edit to that
// package.
//
// A scenario is taken by one test with Claim, because a test that initializes
// a gate in one, pushes to its origin, or reads its tripwires has made those
// facts its own, and two tests sharing one would interfere in a way that reads
// as flakiness. A test that only needs a repository to point a run at clones
// the scenario's origin instead, which is what a person has and which nothing
// else is holding.
//
// Reading the subject back goes through Git, which builds a git command line
// and runs it under the isolation that build used, rather than through
// internal/vcs. That makes this package the second documented exception to the
// rule that internal/vcs is the only package invoking git, internal/fixture
// being the first, and it is taken for the reason that package takes it: a
// harness that confirmed the product's own git operation by asking the package
// under validation would be reporting that package agreeing with itself.
//
// Neither exception weakens the rule. It has one owner and two named
// exceptions, both named for a reason, and a third is a finding to raise
// rather than a precedent to follow.
//
// # The two seams into a separate process
//
// internal/agents resolves the configured agent by name off PATH, and
// internal/forge invokes a provider command. Neither can be given a Go value by
// a harness running in another process, so both are stood in for by a copy of
// this test binary placed on PATH under the name each resolves. ActAsShim is
// what makes one copy behave as an agent and another as a provider, and the
// agent name is deliberately not served there: an agent invocation that
// reached it carried no stand-in control flag, so nothing scripted it, and
// running a test suite as somebody's agent is worse than refusing.
//
// # What this package does not do
//
// It decides nothing about the product and reimplements none of it. Every
// answer it holds the product to is either PRD section 13's own test for a
// principle or a value internal/fixture recorded, and a check that invented a
// requirement of its own would be a second owner of a contract that already has
// one.
package journey
