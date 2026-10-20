// Package forge is the seam between this product and a code host. PRD section
// 8 gives it the provider abstraction for pull requests, checks, and merge
// state, with one interface and one adapter per provider, and PRD section 5's
// pull request and checks stages are what it answers to.
//
// Provider is the interface, and this module has two implementations: GitHub,
// the adapter over the gh command line, and the GitLab adapter over GitLab's
// REST API, which GitLabHost opens. Hosts holds both and is the Host the
// rest of the product is handed: it gives a specifier to the adapter for the
// host the specifier names, so which code host a run reaches is read off the
// run's own record and nothing else.
//
// # A provider addresses one repository, and a Host is how it gets one
//
// A Provider is fixed to a repository, and the thing holding one is not: a
// service resolves its adapters once and then serves every run. A Provider
// settled at that point would address one repository for every run, so this
// package splits the two by lifetime. Host holds what is settled once - the
// command line, the environment, the redactor, the output bound - and
// Host.Open takes the repository, which is the run's own fact and arrives from
// the run's record. RepositoryOf is what reads that specifier out of the
// upstream URL a record holds, through GitHubRepository or GitLabRepository.
//
// GitHubRepository reads a remote on GitHubHostname and no other host, and
// every invocation is given that host in GH_HOST. Both are there for one
// reason: the specifier this package puts on a command line is owner/name and
// names no host, so a remote on another host that yielded owner/name would
// send a pull request to the github.com repository sharing the name, and an
// operator's environment pointing gh elsewhere would move where a write lands.
// What that costs is a GitHub Enterprise installation, which this adapter does
// not address; a run whose upstream is on one carries no specifier, and
// internal/service refuses the run before it starts.
//
// # A write goes where the run said, or it does not happen
//
// Opening a pull request is outward-facing and cannot be taken back, so the
// write operations - Open, UpdateBody and Merge - are preceded by a confirmation
// that the repository the provider resolves this adapter's specifier to is
// that specifier. An adapter that names no repository cannot write at all,
// because there is nothing for the provider's answer to be checked against.
//
// A specifier and the repository behind it are two things, and this package
// cannot see which repository a provider will reach. What the confirmation
// buys is not knowledge of that: it is that a write happens only where the
// provider itself, asked which repository the specifier names, answers with
// that specifier. Where the two differ, for whatever reason, nothing is
// created, and it is that half the guard is for.
//
// Two gaps in it are worth naming rather than implying away. It runs before a
// write and not before a read, so Find, Get and Checks may report facts read
// out of whatever the specifier resolves to; what a caller does with those is
// a write, and the write refuses. And it establishes what the provider
// reported when it was asked, not a lock: anything that changes the answer
// between the confirmation and the write is outside it.
//
// # A pull request from a fork names two repositories
//
// A run whose repository record names a fork pushes its branch there and
// proposes the pull request to the upstream. Host.OpenFork is how such a run
// reaches a Provider: the repository it addresses is the upstream, as Open's
// is, and the fork is a second specifier the adapter holds as the head
// repository. The two are modelled as two fields rather than as one
// owner:branch string, so which repository is pushed to and which one the
// pull request targets are never read out of each other.
//
// Every write still goes where the run said or does not happen. Open, which
// is the write naming both, confirms the upstream and then the fork, each
// against the provider's own answer, and creates nothing unless both hold.
// UpdateBody and Merge name only the upstream's pull request and confirm the
// upstream. Neither confirmation establishes that the fork is a fork of the
// upstream: whether it accepts a head from that repository is the provider's
// to decide, and its refusal reaches the caller as one.
//
// Find is the read that changes. A branch name alone does not identify a pull
// request proposed from a fork, because any repository proposing to the
// upstream can carry a branch of that name, so Find from a fork keeps only
// the open pull requests whose head repository the provider reports as the
// fork. A read that comes back as full as one read may is refused rather than
// searched. What this rests on and does not verify is that gh lists a pull
// request from another repository under its bare branch name; the adapter
// sends owner:branch as a create's head, and that is the form it sends rather
// than a claim about how gh resolves it.
//
// The push itself is not this package's: internal/vcs makes it, internal/safety
// decides it on an anchor observed on the fork, and nothing here invokes git.
//
// # What "the same behavior on any forge" means here
//
// Every state a caller acts on is a typed value declared in this package, and
// a provider state this package does not recognize maps to one of them rather
// than reaching the caller as text.
//
// The claim stops there, because a wider one would be false. Two exported
// fields do carry the provider: a pull request or check URL names the host it
// lives on, and CheckRun.Reported carries the provider's own words for a state
// that was not recognized. Both are there for a person reading a report, and
// nothing enforces that a caller does not read them; what this package
// provides is that it never has to.
//
// # Zero checks is not a pass
//
// A pull request whose check list is empty has no checks registered. That is
// not the same as having checks that passed, and treating the two alike is how
// a gate reports green over a repository whose CI never ran.
//
// Evaluate turns an empty list into VerdictNoChecks, which is not green. The
// one thing that turns it into VerdictPassed is a NoCIDeclaration: the
// positive statement in configuration, PRD section 10's no_ci key, that this
// repository has no checks. The declaration then travels on the result, so the
// evidence a pass rested on stays inspectable rather than being an absence
// nobody can point at.
//
// The declaration has exactly one constructor, DeclaredNoCI, and it takes a
// resolved config.Config. Elapsed time, a repository's history, the presence
// of a workflow file, and the name of a branch cannot produce one, because
// there is no other way to build a value whose Declared reports true.
//
// What this package does not establish is where that configuration value came
// from. no_ci is TrustTrusted in internal/config's key table, so a pushed
// branch cannot set it, and reading the trusted document from the default
// branch is the gate's part of P7. Both happen before Config reaches here.
//
// A declaration decides nothing once checks exist. A repository that declares
// no_ci and then registers a check is judged on that check, and the
// declaration is absent from the result.
//
// # Settled is not the same as running
//
// A check the provider reports as cancelled has a published conclusion, and
// nothing is going to replace it. Waiting on it is waiting forever, so this
// package models the distinction rather than bucketing everything that is not
// a success together. CheckState.Settled reports whether the provider has
// published a conclusion, and CheckState.Passes reports whether a settled
// state stands in the way of a pass.
//
// CheckStateCancelled is settled and does not pass, so a run holding one
// reaches VerdictFailed and can take a fix round or report. It stays a state
// of its own rather than folding into CheckStateFailed, because "cancelled" is
// what a person needs to be told.
//
// The opposite rule applies to a state this package does not recognize. It
// becomes CheckStateUnrecognized, which is not settled, so the caller keeps
// waiting. Guessing that an unknown state is terminal risks acting on a
// verdict that has not arrived, and waiting is the error that can still be
// corrected. What bounds that wait is PRD section 10's checks_timeout, which
// belongs to the caller; this package has no clock.
//
// The zero values are the safe ones. A zero CheckState is
// CheckStateUnrecognized, a zero Verdict is VerdictNoChecks, a zero
// Mergeability is MergeabilityUnknown, and a zero PullRequestState is
// PullRequestStateUnknown, so a caller holding a value it never filled in
// concludes nothing.
//
// # Mergeability and checks are separate answers
//
// Get reports where a pull request stands and whether the provider can merge
// it. Checks reports the checks on its head. They are separate calls returning
// separate types, so "checks are green but the branch conflicts" and "checks
// are still running" are different results rather than one blended verdict.
//
// The GitHub adapter reads the provider's mergeable field and not its
// merge-state summary, because that summary folds check status into the
// mergeability answer and would collapse exactly the distinction above.
//
// # GitLab
//
// The GitLab adapter speaks GitLab's REST API, version 4, through one
// net/http client its GitLabHost holds, rather than through the glab command
// line. Two reasons decided that. The host a request lands on is then a
// constant in this package rather than whatever a command line resolves from
// its own configuration and the environment, so the pin GH_HOST buys for gh
// is structural here. And the answers are GitLab's documented JSON objects,
// which labtest can serve over HTTP exactly as the adapter reads them, so the
// stand-in is wire-level without modelling a second tool's output format and
// exit statuses. What it costs is that this package handles the token
// itself, so keeping it out of every refusal is this package's job: it
// travels only in the PRIVATE-TOKEN header, the endpoint can be nothing but
// gitlab.com or a loopback stand-in, and a redirect is refused rather than
// followed with the token on it.
//
// A GitLab specifier names its host - gitlab.com/group/project - where a
// GitHub one is owner/name. GitLabRepository says why: a specifier is
// compared wherever a repository is named, and a two-segment GitLab path
// would otherwise be the same text as a GitHub repository on a different
// host. In particular internal/config's autonomy.merge lists a GitLab
// project by this specifier, host and all, beside a GitHub repository's
// owner/name, so an entry naming a project's path without its host is a
// GitHub repository and never authority over the GitLab project. The grammar
// both packages hold a specifier to is internal/forge/specifier's.
//
// The facts a caller acts on are held to the same rules as GitHub's, and the
// conformance suite runs both adapters through the same assertions: an empty
// check list is not green, a state the adapter does not recognize keeps the
// caller waiting, a cancelled pipeline is settled and not a pass, a merge is
// bound to the observed head or not asked for, and a write is preceded by a
// confirmation of every project it names. Where GitLab's model differs, the
// adapter takes the strict reading and says so where it reads it:
//
//   - The checks are the merge request's pipelines on its head commit,
//     followed page by page to a bound; Checks says why a skipped pipeline is
//     no check at all, why the commit status list is not read, and what that
//     leaves unread.
//   - Mergeability is read from has_conflicts and merge_status, never from
//     detailed_merge_status, which folds pipelines and approvals in.
//   - A merge sends the head commit and nothing else, so no merge method is
//     chosen here and no merge is scheduled for when a pipeline succeeds.
//     The refusals GitLab's merge endpoint documents include 405 for a
//     merge request it will not merge now, 409 for a sha that is not the
//     source branch's head, and 422 for a merge it attempted and did not
//     make. This adapter does not tell them apart: every status outside
//     2xx but 401 arrives as ReasonRejected with GitLab's status and
//     message, which the adapter's tests establish for those three over
//     labtest, and internal/service's autonomy merge reports it and does
//     not ask again.
//   - Find on an adapter with no fork keeps only a merge request whose source
//     project is its target project, which is stricter than the GitHub
//     adapter's read of the same case.
//   - Neither adapter reads approvals or discussions. A merge GitLab refuses
//     for either arrives from Merge as ReasonRejected, carrying its message.
//
// Its residual gaps, beyond those it shares with the GitHub adapter:
//
//   - None of this was exercised against gitlab.com. The request shapes, the
//     status words and the error object are taken from GitLab's API
//     documentation, and labtest answers with those shapes; a field GitLab
//     renames arrives as its zero value, which reads unknown, unrecognized or
//     malformed rather than as a pass.
//   - A self-managed GitLab installation is not addressed, for the reason a
//     GitHub Enterprise one is not.
//   - The proxy settings in the service's environment apply to the client,
//     as they do to any net/http client; certificate verification is the
//     standard library's default and is not relaxed.
//   - Telling a permission refusal from an authentication failure rests on
//     the status GitLab answers with; a 401 for any reason is
//     ReasonUnauthenticated.
//   - The token's value is not a shape internal/redact recognizes, so what
//     keeps it out of a refusal is that nothing here puts it there, not a
//     redactor that would catch it if something did.
//
// # A provider that cannot answer refuses
//
// Every failure is a *Refusal naming a Reason, and a refusal is a typed result
// rather than a warning execution continues past. A provider that could not be
// run at all is ReasonUnavailable, and one that reported an authentication
// failure is ReasonUnauthenticated; neither is reported as an empty list of
// checks or a pull request that does not exist.
//
// Telling those two apart rests on what the provider reports. The GitHub
// adapter keys ReasonUnauthenticated on gh's own authentication exit status
// and does not read gh's messages looking for words, so an authentication
// failure a provider signals some other way arrives as ReasonRejected carrying
// the provider's message rather than being guessed at.
//
// # Credentials
//
// PRD section 8 gives credential removal to a redact module, which is
// internal/redact. This package writes no redactor of its own, per P14:
// NewGitHub requires a vcs.Redactor, the seam internal/vcs already declares,
// and every piece of provider text that reaches a Refusal passes through it.
//
// Outbound text goes through the same Redactor. Everything this package sends
// a provider - the argument vector and the standard input of every invocation
// - is redacted at the one point invocations pass through, so a pull request
// body or title built from stage output does not publish a credential a stage
// saw. runExact owns that argument, what the redaction costs, and what it does
// not cover.
//
// Two structural measures sit under that, because a redactor is a filter and a
// filter is a thing that can be handed the wrong pattern. A repository is
// addressed as owner/name, and a specifier carrying a scheme or userinfo is
// refused before gh is invoked, so this package never puts a credentialed URL
// on a command line. And the argument vector is not copied into a Refusal at
// all, so the only provider text a caller can receive is what the provider
// wrote, redacted.
//
// The residual gap is worth naming: a credential this package never sees can
// still reach gh's own output, through a credentialed remote in the
// repository's git configuration, and what removes it there is the supplied
// Redactor and nothing else. A caller that supplies one recognizing fewer
// shapes than its secrets take has that much less protection, and this package
// cannot tell.
//
// # What this package does not do
//
// It does not decide whether to merge. Provider.Merge asks the host to merge
// one pull request bound to one head, and internal/service's autonomy merge
// is its one caller and the place that decides when to ask; whether the merge
// is allowed stays the host's, as PRD section 9 keeps merge policy with the
// forge and the operator.
//
// It does not poll. Checks reports what the provider says now, and how long a
// run waits, when it re-reads, and when it gives up belong to the checks stage
// and to checks_timeout.
//
// It does not own a process tree. A provider invocation is one short-lived
// process, and a context that ends kills that process.
//
// What the grace deadline on every invocation buys is that the call is
// released either way. Killing the provider does not close the output pipes a
// descendant inherited, and waiting for those is not a wait a context ends, so
// without the deadline a descendant that stalls could hold Checks or Get open
// indefinitely and the caller's checks_timeout would bound nothing. With it,
// that costs at most DefaultProviderGrace and then the invocation refuses.
//
// The residual gap is that the descendant itself is not pursued. Nothing here
// signals it and no process group is terminated, which is what internal/agents
// does for an agent, so a provider command that leaves long-lived children
// behind leaves them running after the invocation has returned. What that
// costs is a stray process rather than a stuck run.
//
// It does not write a pull request body. What a body says is the pull request
// stage's to decide; this package carries the text it is handed.
//
// # Requirements
//
// For a github.com repository, the gh command line on PATH, or one named with
// WithBinary, authenticated for the repository being addressed. For a
// gitlab.com project, an access token given to WithGitLabToken, which
// internal/service reads from GitLabTokenVariable, with the scopes the
// project's API needs.
//
// # The stand-ins another package tests against
//
// labtest is the same for the GitLab adapter: a stand-in GitLab API on a
// loopback address, answering with GitLab's documented objects, which the
// real adapter reads over HTTP. The conformance suite here runs the GitHub
// adapter over forgetest and the GitLab adapter over labtest.
//
// forgetest is a stand-in gh answering over the wire, read by this package's
// real adapter, scripted with only shapes gh puts on standard output, on
// internal/judge/judgetest's terms: a test outside this package that needs a
// merged, open or closed pull request builds a Host over it and never a
// forge.PullRequest by hand. This package's own tests keep their private
// stand-in, which also models a killed provider, one holding its pipes
// open, and the credential tripwire, and moving them is not the small
// change forgetest is.
package forge
