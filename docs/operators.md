# Operating the gate

This is a short guide for the person or agent that pushes work through the gate
and for whoever owns the repository's CI and merge settings. It describes what
this build does today. [`prd.html`](prd.html) is the specification, and where
this page and the code could disagree, the code and the PRD win.

## Start here

This is the detailed **operator reference**, not a short tutorial. Use the [README](../README.md) for a product overview and quick start; use this page when commands, configuration trust, safety decisions or failure recovery matter. The [PRD](prd.html) remains the contract.

| Your question | Read |
| --- | --- |
| How do I set up the gate and send a change? | [Push through the assistant remote](#push-through-the-assistant-remote) and [A first run, step by step](#a-first-run-step-by-step) |
| What should I do when execution stops? | [Answering a hold](#answering-a-hold) and [Bounding what one run spends](#bounding-what-one-run-spends) |
| How do I run and supervise a fleet? | [Catching up on a fleet](#catching-up-on-a-fleet), [Waiting on a fleet](#waiting-on-a-fleet), and [Delegating a domain](#delegating-a-domain) |
| How is unfinished work protected? | [Kept work](#kept-work) |
| Can it merge without me? | [Unattended operating posture](#unattended-operating-posture), [Keep your CI required](#keep-your-ci-required), and [Merge order and stacking](#merge-order-and-stacking) |
| What is not guaranteed? | [What a pass does not cover](#what-a-pass-does-not-cover) |

**Safety reading rule:** A run **holds** when it needs a person's decision and **parks** when a bound ends its progress. Missing evidence is not success; do not bypass a refusal by assuming the underlying state. The details below are intentionally retained for agents and operators making decisions.

## Push through the `assistant` remote

`assistant init` creates the repository's gate and adds a remote named
`assistant` to the working copy. Pushing a branch there is how a change is
handed over:

```sh
git push assistant BRANCH
```

`init` also records the repository's default branch, which is where trusted
configuration is read from. It takes the branch this working copy records
origin's HEAD as naming, reads nothing over the network, and never takes the
branch you are standing on. When the working copy has no origin, records no
HEAD for it, or records one pointing outside origin's branches, `init` refuses
and sets up no gate; record it with `git remote set-head origin --auto`, or name
the branch with `--default-branch`. Either way `init` says which branch it
recorded and where that answer came from.

`init` takes the upstream, where validated changes go, from the working copy's
origin. When there is no origin and `--upstream` names none, `init` refuses,
sets up no gate, and exits with the incorrect-usage code (2). Name the
repository with `--upstream`; a working copy with no origin also has no default
branch to learn, so give `--default-branch` beside it. When origin sends a push
to another repository than the one it fetches from (a push address set with
`git remote set-url --push`), `init` refuses the same way, because your own
push and a run would go to different places; name the one your changes are
destined for with `--upstream`. Two addresses for one repository on github.com
or gitlab.com, such as HTTPS to fetch and SSH to push, are not refused.

`init` also prints the row `doctor` prints for the upstream it recorded: one
that opens pull requests on github.com, merge requests on gitlab.com, one with
no code host, or a host this build does not address, which a run is refused on
and which `init` records anyway so you can correct it with `--upstream`.

### Running `init` again

Running `init` again is how a registration is repaired or changed, and it adds
no second gate or record. It writes the gate's hooks, its record and the
`assistant` remote again and keeps every run recorded through it. Its first row
is the new-gate row only when a gate repository was created at this path; after
a move or copy it is the reattachment row; when the checkout already had its
gate here it is the row that says init set the gate up again where it stood. It
records what it was given this time, and for each field of the repository record
that now holds a different value it prints a row: the upstream, the default
branch (with the one it replaced after `Default branch before`), and the fork. A
fork is recorded only while `--fork-url` names it, so an `init` run again
without the flag stops sending runs to your fork and says so; run it with
`--fork-url` to keep it. `--json init` carries the same facts as `created`,
`reattached`, `changed` (each with `field`, `before` and `after`, credentials
removed) and `upstream`: `created` is true only when a new gate repository was
made at this path; `reattached` is true when a gate was adopted after a move or
copy (`created` false); when the checkout already had its gate here, both are
false. Run again with the same flags, including `--fork-url` when a fork is
recorded, and an unchanged origin, and it reports no change.

When git cannot be run, `init` and every other command that reads the checkout
say that rather than that the directory is not a checkout, set up nothing, and
exit with the operational failure code (1). `assistant doctor` reports it as a
`git` row ahead of the rows that read the checkout, and makes none of those
rows until git can be run.

The test and lint stages run the commands the trusted configuration names and
hold for you when it names none. To shorten that setup, `init` reads the
checkout's files for the commands the repository already declares (a
Makefile's `test`, `check` and `lint` targets, `go.mod`, `package.json`
scripts, `Cargo.toml`, `tox.ini` and `pyproject.toml`), runs none of them, and
offers them. At a terminal it asks about each one; elsewhere it only prints
them, and `--accept-commands` writes them all. They are merged into the
checkout's `assistant.json` and never replace a key it already names. Runs read
commands from that file only as the default branch on origin holds it, so
commit the file and land it there before expecting a run to use them.

`init` also reads the default branch's copy of `assistant.json`, as origin's
copy of that branch was last fetched here (or the local branch of that name),
without fetching. It offers nothing for a key that copy already names, even
with `--accept-commands`, so it cannot make the two copies disagree, and lists
those keys after a row saying so. When both copies name a key with different
commands it says the checkout's command reaches no run until it lands there.
When the default branch's copy does not parse it says so and offers as if
there were none; a run refuses that copy on its own terms. None of this stops
the gate from being set up. A branch that moved on origin since the last fetch,
or an `--upstream` that is not origin, can name a key this read did not see.
`--json init` carries it under `commands.branch`. Nothing
is offered for `commands.document`: no common build file declares a command
that updates prose, so that stage keeps holding until you configure one or
answer it.

### A first run, step by step

Once `init` has run, `assistant doctor` also reads where the checkout's change
stands and prints its next step after the verdict, one step at a time, in the
order a first run meets them. Do what it says and run it again:

1. On the default branch, or on no branch, it names `git switch -c` with a
   name of your choosing. Uncommitted work comes along to the new branch.
2. With work no commit records, it prints a row for each kind present - an
   unresolved conflict, untracked files, unstaged edits, staged edits - and
   names `git add` and `git commit` as each one needs. A run validates only
   what is committed.
3. On a clean branch with nothing its default branch lacks, it says there is
   nothing to send yet. It compares against origin's copy of the default
   branch as this checkout last fetched it, or the local branch of that name
   when there is no such copy, and fetches nothing.
4. On a clean branch with commits the gate does not hold, it prints the exact
   command, `git push assistant BRANCH`, with the branch name quoted for a
   shell when it needs to be, and says to follow the run with
   `assistant watch`.
5. When the gate already holds the branch's commit, it says so and names
   `assistant watch` and `assistant rerun`. When the branch no longer holds
   what the gate holds for it, as after an amend, it says sending replaces
   that and prints the command with `--force-with-lease`; whether to run it is
   your decision.

The gate's side is read from the gate repository itself, never from this
checkout's `assistant/BRANCH` tracking reference. Before `init` none of this
is read, and the gate row's "Run assistant init here." comes first. `--json
doctor` carries the same facts under `change`: the step, the branch and
default branch, the four indicators, and the command. Doctor writes nothing to
the checkout or the gate; every step is a command you run (P1).

The gate's admission hook decides whether the push may proceed, and its
notification hook starts a run for that branch and commit. A push that deletes
a branch, or carries anything that is not a branch, starts nothing. An ordinary
`git push` to your other remotes is untouched, so a branch that is never pushed
to `assistant` is never validated. Nothing here stops a person or agent from
pushing elsewhere; the gate is the inner check for work that is handed to it,
not a barrier around the repository. A forge-side branch protection rule is what
enforces that for everyone.

Admission also decides who is pushing, from the kernel's record of the
pushing process and its parents and never from anything the push says, and
it admits a push from exactly two kinds of sender. A task's worker, placed by
the service's worker set, is held to its own branch, forward, never a
deletion. A shell at a terminal is a push with a process in the foreground
of its controlling terminal somewhere on its chain: your `git push` typed at
a shell, a script that shell runs, a job it backgrounds while it holds the
prompt, and a command a coordinator agent's host runs in your terminal all
qualify, because the chain is read and not only the pushing process. A push
the service can place as neither is refused with code `unplaceable` before
any reference moves, and the refusal names every process it read: a cron
job, a daemon, an `ssh` session with no pseudo-terminal, `git-receive-pack`
under `sshd`, a GUI client with no terminal, and a process whose starter
exited so the kernel moved it under `init` are all that shape. Push again
from your shell. What the service establishes is a terminal's foreground
above the push and not a person: a program that allocates a terminal of its
own and pushes from it is admitted as a shell's, and the check does not know
who is at the keyboard.

A run walks nine stages in a fixed order: intent, rebase, review, test,
document, lint, push, pull request, and checks. Each stage that cannot decide
holds for a person, and `assistant status` and `assistant doctor` say where a
run stands and what stops one.

Fix rounds and the rebase stage record commits in the run's isolated copy,
which is a linked worktree of the gate repository, so they take the committer
identity git resolves there: your global or system configuration and the gate
repository's own, not the working copy's. With no `user.name` or `user.email`
in effect, such a step fails with git's own refusal, and nothing supplies an
identity in its place. `assistant doctor` asks git the same question in the
gate repository and reports the answer as its "commit identity" check, with
`git config --global user.name` and `git config --global user.email` as the
remedy. The check does not stop a run from starting, and it answers for the
environment doctor runs in, so an identity set only in variables such as
`GIT_AUTHOR_NAME`, or a different `GIT_CONFIG_GLOBAL` for the service, can
make it disagree with what a run meets.

## Answering a hold

`assistant --answer approved`, `skipped`, or `cancelled` answers the decision a
run is holding on. At a stage before push you can also ask for one fix round:

```
assistant --answer fix --select ID[,ID...] [--note TEXT] [--finding TEXT]
```

A task's own hold (an investigation report's among them) is answered with
`--task`:

```
assistant --task ID --answer TEXT [--hold KEY|FINDING] [--note TEXT]
assistant --task ID --run --answer TEXT
```

`--hold` names which open hold to close (the hold's key, or a finding id under
that task); without it the only open one is answered, and more than one open
is refused and listed. `--note` records free
text with the resolution about what was decided; who decided stays the surface,
never a claim made by the note. `--run` answers the task's current run even
while the task still has holds of its own open, so a promoted delivery run is
reachable without first clearing every report hold.

A delivery task's posture is recorded at intake or promotion from the
`delivery` key, and only your own configuration file, `config.yaml` under
the home, can loosen it: `delivery: local` there lets every repository of
the home record `local` tasks, and with the key absent the posture is
`gated`. A repository's
`assistant.json` may name a stricter posture, which takes effect for that
repository, and never a lighter one: a trusted copy on the default branch
saying `local` while your file says `gated`, or says nothing, refuses every
intake, promotion and run of that repository, naming the trusted layer and
both postures, until either file changes; a pushed branch saying `local`
over a `gated` beneath it refuses its own run the same way. The file has no
per-repository section, so `local` in it is consent for every repository
that does not tighten it.

A local delivery task's approval hold, keyed `task/ID/local-approval/...`,
is yours and not the coordinator's: `approve` fast-forwards your local
default branch to the worker's commit, `stop` leaves it where it is. The
service takes either answer only from a command in the foreground of a
terminal, which it reads from the kernel's record of the calling process
and never from anything the command says: type it at your shell, in the
foreground. A command with no terminal (a script, a cron job, `ssh` with no
pseudo-terminal), a background job, or a command a coordinator agent's host
runs is refused with code `not-at-terminal`, the hold stays open and nothing
moves. What the service establishes is a terminal's foreground and not a
person: a program that allocates a terminal of its own and runs the command
there is served, and the record of who approved still says
`machine-interface`. On a platform where the service identifies no socket
peer, every such answer is refused.

The stage runs again after the round and holds again, so you see the result
before anything is pushed. A round that changed nothing says so and returns to
the hold. At most three rounds can be asked for at one stage in one run. The
push, pull request, and checks holds do not offer a fix round: push forwards a
commit, so a round there would be pushed before you saw it, and a round after
push would leave a commit nothing pushes.

When the branch already holds commits the commit you submitted does not
contain, somebody else pushed them, and the rebase stage replays the run's
own commits onto the branch as it observed it rather than onto the base, so
the push is a fast-forward and their commits survive it. It does that on its
own only when their commits and your change touch different files. A
colleague's edit to a file your change also changed always holds the rebase
stage for you, as `rebase-branch-overlap`, naming those files, before anything
is replayed. That is the price of never placing one side's edit by matching the
other's lines, which a merge, a merge driver or a patch offset can each get
wrong without a conflict. The remedy is to bring their commits into your
change yourself and push again.

Three other cases hold too. `rebase-branch-replay-unverified` means the
replayed result is not exactly their branch with your files' versions in it,
at the paths the hold names, for example because git left one of your
commits out as already applied or dropped a mode change. `rebase-branch-conflict`
means a replay over different files still stopped on a conflict. In both, no
replay is carried on and the run's head is the one you submitted.
`rebase-branch-behind-base` means the branch does not contain the current
base, and bringing the base in would rewrite their commits: approving carries
the run on from the branch without the base's newest commits. Approving past
`rebase-branch-overlap`, `rebase-branch-conflict` or
`rebase-branch-replay-unverified` reaches the push stage's refusal below.

A push hold whose finding is `push-refused` with reason `would-discard` means
the branch holds commits the run's commit does not contain and that the run
would lose by pushing. Either the branch moved after the run observed it, or
it already held them when the run began and the run was carried past the
rebase stage's hold without them: somebody else pushed to it, and the commit
you submitted neither contains their work nor stands on it, including a
commit that is behind the branch. A run only replaces commits that were in
the commit you submitted, or in what it pushed earlier itself, which is what
its own rebase rewrites. The finding names the commits it would lose,
counting rather than listing any past a fixed bound, and states the remedy:
observe the branch again, rebase onto the commit that observation found, and
resubmit anchored to that new observation. Nothing in this build follows that
remedy for you inside the held run; approving the hold advances it without
pushing.

The hold lists only the findings that are holding the stage. `--select` takes
the identifier of any finding the stage reported, including a fix or note
finding the hold does not list. `assistant --json status` lists every one, with
its identifier, under `active_run.stages[].report.findings`.

## How a task's worker reports back

A task's worker tells the program about its work with one command,
`assistant worker`, run from its own copy. The service starts every worker
with that command's full prefix: this binary's absolute path, `--home`
naming the service's home, and `worker`. That prefix is what Claude Code
and Cursor ACP workers are allowed to run besides git without asking, and what
its instructions tell it to type. Through it a worker acknowledges a
coordinator's message by the number it arrived under, files an
investigation's report once, and records how its work stands. The
instructions tell an investigation's worker to write its report with its
file editing tool into `assistant-report.json` at the top of its copy and
file it with `worker report --file assistant-report.json`, because the
Claude Code CLI's Bash check refuses a report written inline as a
here-document. The command removes the file once the report is filed, so it
does not stay in the copy; a report the service refuses leaves the file in
place to be corrected. `--file -` still reads the report from standard
input.

What follows for whoever runs the service:

- The prefix is fixed when the service starts. A binary moved or replaced
  afterwards, or a home moved, is not seen by a running service's workers
  until it is restarted.
- The service decides who the caller is the worker of from the socket's
  credentials and the caller's parent chain, never from anything the
  command says. Running the same line from your own shell is refused as no
  task's worker, and nothing is recorded. On a platform where the service
  identifies no socket peer, every call is refused that way, a worker's
  included.
- A filed report's decisions are holds you answer with
  `assistant --task ID --answer ...` (optionally `--hold` and `--note`; see
  Answering a hold). The investigation's copy is removed only after its
  worker has ended, which it does when it exits on its own or when the
  coordinator stops it, and only on the cleanup check's proof. A report
  filed by a worker that is still running keeps its copy.
- Beside its findings and evidence, a report may carry `uncertainties`,
  what the worker could not settle, and `follow_ups`, further work it
  recommends, each a list of `{"text": ..., "location": ...}` with the
  location optional. Neither is a finding: they register no hold, start
  nothing, and do not keep the task `held`, so a question the worker needs
  a person to decide has to be an `ask` finding. A report that puts an
  `action` on an uncertainty or a follow-up is refused (the defect
  `action-outside-finding`, naming the entry) and nothing is filed. A
  credential written into either is removed before the report is stored,
  as it is from every other part of the report. The plain `assistant tasks`
  line counts each list on a line of its own and never prints their text;
  `assistant --json tasks ID` carries both under `report`.
- Once its report is stored, an investigation reads `reported` whether its
  worker is still running or has ended, and `held` only while a decision the
  report raised is open. A worker that ends after filing is not a crash: a
  restart does not relaunch the task, and supervision does not queue a
  `terminal-gone` wake for it. An investigation that ends without filing a
  report still reads `unknown` and is still reported and relaunched as a
  worker that died.
- The stored report survives a service restart: after stop and reopen,
  `assistant --json tasks ID` still reads `reported` (or `held` while a
  decision is open), the same open holds, and the same report body at
  `report` (`summary`, `findings`, `evidence`, `uncertainties`,
  `follow_ups`, `evidence_file`, `recorded_at`). The plain
  `assistant tasks` line does not print the report; it points at that
  `--json` read, because the report is evidence to open rather than text
  to relay.
- Filing a report releases a task the coordinator queued behind the
  investigation as soon as it reads `reported`, without waiting for a run
  to finish or a restart. One whose report raised decisions reads `held`
  until they are answered, and what is queued behind it is released at the
  next of those instead.
- A reported investigation reads `complete` only once the coordinator
  records its findings relayed with `assistant coordinator relayed ID`. The
  service then ends the worker it holds for the task and removes its copy on
  the cleanup check's proof; the stored report and its holds stay.
- An investigation copy is cut with no remotes and a detached HEAD, and the
  launch asserts that again before a worker starts. A push its worker makes
  to the gate, by any remote name or by the gate's path, and whose pushing
  process the service places as that task's worker, is refused before any
  reference moves, whatever it carries, because the task names no branch; a
  delivery worker's push is held to its own branch, forward and never a
  deletion. If an intermediate process in the push chain exits first and the
  kernel reparents the chain, placement reads the push as nobody's and
  admission refuses it with code `unplaceable` rather than applying either
  rule; the commit stays in the worker's copy, and a push the worker runs
  and waits for is admitted under the worker rule. That
  is the product's whole reach (P11): the launch assertion, and admission
  for the pushes that arrive at the gate. A worker
  that later adds a remote toward the subject, or pushes by the subject's
  path, is neither re-checked nor seen by the gate. Such a push can move a
  protected branch tip when that branch is free to update on the subject; a
  tip that stays put because the branch is checked out is git refusing the
  update, not a continuous isolation monitor here. Promotion to delivery is
  a separate, coordinator-only step; a worker that calls
  `assistant coordinator promote` is refused.

## Curating project knowledge

A repository's project knowledge is one file, `assistant-knowledge.md` at
the top of the repository: the conventions you want every worker and every
reviewer told, such as which command proves a change, which directories are
generated, or which module owns what. You curate it like any other file and
it takes effect when the change to it lands on the default branch.

Where it is read from is the whole of what makes it curated. The program
reads it only from the default branch, at a commit it fetched from the
upstream for the read, the same trusted copy `assistant.json` is read from
(P7). It never reads the copy a pushed branch carries, a working copy, or
anything an agent wrote. So a branch that rewrites the file is not reviewed
under its own rewrite, an agent cannot make its own claim into project
knowledge, and the audit of who changed the file, when and why is the
repository's own history.

What reaches whom:

- A task's worker is told the file in its first turn, after the task's
  instructions, framed by two lines naming the digest of its bytes and the
  default-branch commit it was read at. Each launch reads it afresh, a
  relaunch included, so a worker relaunched after a merge is told the newer
  revision. The task's attempt records that commit and the SHA-256 of the
  file there, `knowledge_commit` and `knowledge_digest` on the task's
  `task_attempt` row in the home's database, and nothing of the text. Those
  fields name what the default branch held there; reproducing what the worker
  was told takes the same credential removal the program applies, because the
  section is redacted text, not the raw file. `git show
  COMMIT:assistant-knowledge.md` is the committed bytes. A known commit with no digest
  means the default branch held no file then. No command prints the record
  yet; it is read from the database.
- A run's review is told the file as the default branch held it at the
  commit the run's configuration was read at. The push stage's review of the
  run's own later commits is told the same. A repository whose trusted
  configuration sets `suppress_project_instructions` is told none, because
  the file is the repository's own instructions.
- Nothing else is told it. A fix round's fixer is told the findings it is
  asked to resolve and not the file.

The text passes the same credential removal every stored or relayed text
does before it reaches anyone, so a URL with a credential in it or a token of
a shape the program recognizes arrives as `redacted`. Do not put secrets in
the file anyway: the removal recognizes a fixed set of shapes and no others.

A repository with no file composes nothing and refuses nothing. A file
larger than 32 KiB, or one that is not UTF-8 text, is refused rather than
cut: a launch records `knowledge-refused` and the task reads `failed`, and a
run does not start. With the tmux session provider and an adapter that
places the first turn on the terminal launch command, a file within 32 KiB
can still refuse a launch when the composed first turn exceeds that command's
bound; that is recorded as `knowledge-refused` too, naming the launch limit.
A default branch the launch cannot fetch leaves the task
dispatched with no worker started, the way any other launch read that fails
does, rather than starting a worker told nothing. The file is context and grants nothing: it does not widen what a
worker may run, does not change what the gate checks, and says so to the
agent it is given to. Whether an agent follows it is the agent's; no check
reads its work against it. Each copy is a checkout, so an agent can still
open its own copy's revision of the file itself; what the program controls
is what it tells the agent.

Session notes for one task are not project knowledge. Steer a running task
with `assistant coordinator message ID --text ...`, which is recorded,
delivered under a number and acknowledged by the worker; nothing a message
says reaches another task or the file.

## Catching up on a fleet

`assistant watch` starts with one read of the fleet and then follows the
service's updates; `assistant watch --once` prints that read and exits. When the pull guard
has a supervision warning for in-flight work, that warning is printed first;
otherwise the read opens with a digest. That digest is one line counting what follows it:

- the decisions waiting on you, every open hold and every run held at a
  decision;
- what was merged or cleared away since you last marked what you had seen;
- the work under way, and the changes being validated right now;
- the work that stopped without asking you anything.

The four parts follow, each with its own line per item. The digest counts
those parts and nothing else, so it can never disagree with them; it starts
nothing, answers nothing, and acknowledges no wake.

A task whose worker last said it is at work, while no live terminal was seen
for it on this read, is not counted as moving: the digest says how many such
tasks there are, and the task's own line says the worker was not seen at it.
That line is the same in `assistant tasks`. Only a worker whose terminal was
observed live reads as a worker on the work.

`assistant watch --once --mark-seen` marks as seen exactly the merged and
cleared-away work that read showed, so the next read, and every read after
it, counts only what landed since. Nothing else is filtered: a decision is
counted until it is answered, and what is under way or stopped is counted for
as long as it is true.

`assistant --json watch --once` carries the same counts under `digest`
(`waiting`, `landed`, `under_way`, `unconfirmed`, `advancing`, `stopped`),
computed from the parts in the same document. A task that resolves as unknown
is both under way and stopped, so it is counted in both, and the counts are
not meant to be summed. Agent usage is not in the read: it stays the usage
view, `assistant runs --usage`.

### Kept work

When a run's or a task's isolated copy is kept because its work is not on
the shared branch or anywhere else the cleanup check can prove it safe (no
proof held, or the copy holds changes, files or a stash no commit records),
the service records a kept-work entry for it: whose copy it was, the
project and branch, the commits the copy was read to hold, and why it was
kept. A restart does the same for a finished run's copy it finds still
standing. Each open entry is listed after the four parts of the
catch-up read (`kept` in `--json`) and in `assistant status`
(`kept_work` in `--json`): a working copy's status lists the entries whose
repository is that working copy's, and every status lists the entries whose
owner names no repository, such as an investigation task started without
one, because those belong to no working copy. Each entry also raises one
`kept-work` wake, which reaches the coordinator and the notify command like
any other wake: about the task, for an entry about a task or about a run
attributed to a task, and otherwise about the entry itself, for an entry
about a run no task names, such as the run your own push started. The
second is a wake of its own shape: the notify command's line names the
entry in `kept_work` and its `project` and `branch` rather than a
`task_id`, `assistant coordinator wakes` lists it under the project and
then the branch, and the coordinator acknowledges it as `kNUMBER`, a
number of its own. Acknowledging the wake answers nothing: the entry is
listed until it is answered or closed.

If the copy's work later turns up somewhere safe, for example once you push
its commit to the upstream branch, the next cleanup of it removes the copy on
that proof and closes the entry as removed on a proof, recorded as the
program's and never as a discard; a closed entry is no longer listed. For a
run's copy the next cleanup is the next service start. For a task's copy
whose worker has stopped, the service asks again every five minutes, at most
six times for that stopped worker, and again when the coordinator stops the
task or the service starts. A delivery whose commits the gate rebased before publishing them is
followed through the gate's own record of that rebase, so the copy, which
still holds the commits under their old identifiers, is removed once the
published ones are on the task's upstream branch or in the merged pull
request and hold everything the copy's commits held. A rebase that left
something out, such as a file only a merge commit added, is not recorded,
and neither is a later rebase once a fix round took the delivered change
out or rewrote its lines, so the copy is kept. When the copy is kept
instead, the service log names each check and what it found. Nothing else removes a kept copy. A
service that stops between removing the copy and marking it removed leaves
the entry open; the next start completes the mark from the recorded proof
and closes it. An entry whose copy is gone with no proof recorded, a copy
removed by hand among them, is named in the service log at each start and
stays open.

`assistant eject --confirm` is refused while the repository has an open
kept-work entry, before the gate is touched, and the refusal names each
entry's copy, whose it was and the commits it holds. Until answering kept
work ships (#411 part 2), the way past it is the work landing: once its
commits are on the upstream branch or in the default branch, the next
service start removes the copy on that proof and closes the entry. Part 2's
discard will be your other way.

Eject is also refused, before the gate is touched, while a `kept-work` wake
about one of the repository's entries is still unacknowledged, even once
the entry is closed, because the wake stays actionable until the coordinator
acknowledges it, and forgetting the repository would delete it. The refusal
names the wake by its number; once the coordinator acknowledges it as
`kNUMBER`, eject removes the closed entry, its wake and the wake's
notification record with the run.

Eject is also refused while any run copy of the repository still stands on
disk, whether its run's cleanup has not decided it yet or kept it, and the
refusal names each copy: a run's copy is cut from the gate, so removing the
gate would leave the copy unable to read its own commits. Cancelling a run
asks its copy's cleanup at once, so a copy whose work is on the upstream is
removed then. Once eject has checked that the repository can be removed, it
reserves the removal, and no new run of the repository starts until the
eject finishes; a service that starts after an eject died releases that
reservation.

While a project has an open kept-work entry, no new task in that project
starts: the task is recorded, `assistant --json tasks` shows it waiting and
names the entries under `kept_work`, and it starts on its own once every
entry is answered or closed. Kept work in one project never holds another,
a task whose worker was already launched is not held (its relaunch goes
ahead), and your own push always starts its run, whose answer names the
project's open entries.

An entry is answered the way a hold is: `assistant --task ID --answer land
--hold kept-work/N` for a task's copy, or `assistant --run=RUN --answer land
--hold kept-work/N` for a run's, where `kept-work/N` is the entry's key and
`assistant --json status` shows the entry's number and owner. This build
takes land: the copy's commit is brought into the gate and a run of the
entry's branch starts at it, as a push to the gate would start one, and the
entry's tasks start. A landing decides from the copy as it stands when you
answer, by the same reading a discard shows you, never from what the entry
recorded when the copy was kept. It carries the copy's HEAD and commits
only, so it is refused, recording nothing, when the copy holds a path no
commit records (a changed, new, staged or deleted file, one git ignores
included, each named), a stash, or anything HEAD does not carry that the
default branch lacks: a commit on another branch, one only a reflog
records (an amended or rebased-away commit among them), or a tag the
upstream does not have. What the upstream already holds (its tags and
branches, cloned or fetched into the copy) is not refused, and neither is
a file git reports clean because only its line endings or filters differ.
Put that work on the branch, or remove it (`git reflog expire` for a
reflog's record), and answer again. It is refused for an investigation's copy too,
whose delivery is a promotion. The entry stays open and listed until the
run has started, and only then is it answered land; if the run cannot be
started, the answer is refused naming why and the entry stays open for
another answer. A land answer a stopped service recorded and did not carry
out is carried out at the next start, checked again the same way.

Carry on, `--answer carry-on` with the same flags, creates a new task of
the same project, shape and branch whose copy is the kept copy, moved to
the new task's own place and asserted the way every task's copy is before
anything is written into it; the task the copy outlived keeps its record,
the new task's instructions are the old intake text, or for a promoted
task the intent it was promoted with, with a note saying what is already
there, and the task dispatches into the copy as any task
does. The entry stays open and listed until the new task owns the copy,
and only then is it answered carry on; if the new task cannot be created
(its role is no longer configured, say), nothing is moved, the answer is
refused naming why, and the entry stays open for another answer.
Carrying on points the gate's remote at the gate and puts HEAD where
the task's shape has it, and removes no remote and moves or renames no
branch, since any of that can leave a commit with nothing naming it: a
copy with any other remote (any remote at all, for an investigation's
copy), or a delivery copy whose task branch stands somewhere other than
HEAD, or whose task branch cannot be created at HEAD because a branch of
the copy's is in the way of its name (a branch `topic/side` beside the
task branch `topic`, or `topic` beside `topic/x`), is refused with
nothing recorded and the refusal says what to change. A run's copy is not
carried on in this build (it is a worktree of the gate on a branch of
yours, which a task's copy cannot be): land it, push its commit yourself,
or discard it.

Discard, `--answer discard` with the same flags, is given twice. Alone it
shows exactly what removing the copy would lose, in the structured answer
(`--json`): what neither the default branch nor the upstream holds,
the only two places counted as keeping work. That is the
commits anything in the copy names that neither the default branch nor
the upstream holds, a commit reset away, one only `ORIG_HEAD` or an old
reflog entry still records, and every stash included, and any tag, tree
or blob a reference or a reflog entry points at directly that the
upstream does not have; what the upstream holds now is held and not
listed, a commit one of the copy's remote-tracking branches names
included while the upstream's branch of that name still reaches it, so
one whose branch the upstream has since deleted or rewound is listed (for a run's copy,
only what its worktree names counts: a commit only the gate's own branches
or their reflogs record is not listed, since removing the worktree leaves
them, but they keep nothing either, so a commit the worktree's own HEAD,
references or their reflogs record is listed even when a gate branch has
it too, and so are the stash entries made in that worktree); every file
removing the copy destroys whose content or deletion nothing else holds,
as git would store it, so a file git reports clean is not listed though
its line endings differ, and files git ignores included, each with why (`changed`, `new`, `staged`, `deleted` for
a file the copy deleted, staged or not, or `repository` for a
file of the copy's repository directory, a hook or its configuration
among them, that differs from what git writes into every new repository),
a directory lost whole named once with a trailing slash; whether git
reports a change, a file or a stash; and a digest of exactly what is
shown, each file by what it holds. It records and removes nothing. Given
again with `--confirm DIGEST` it deletes what you were shown and nothing
else, and only while everything shown is as it was: a commit, an edit, a
file or a stash made since is refused and you are told to look again, and
so is a commit you were shown that the default branch or the upstream has
taken up since, since the loss is then not the one you confirmed; a change
that loses nothing more, such as a branch made at a commit already listed,
is not. It first sets aside, beside the copy so it can be put back, only
what the removal needs gone of what you were shown (files, index entries,
and for a task's copy its lost branches, tags, stash and HEAD with their
reflogs), then reads the copy again, and removes the copy only when
removing it would lose nothing you were not shown: a run's copy by git's own
worktree removal without force, which refuses when anything is left, and
a task's copy only when one of the cleanup proofs then holds for what
remains, so a task's copy whose remaining commits no proof covers (for
example a branch the upstream holds under another name) is refused and
stays. Anything written into the copy after your last look, even while it
is being set aside, refuses the discard and everything set aside is put
back; only what a program writes through a file it already had open there
as the discard moves that file aside is lost with it. If something was
written exactly where a file set aside goes back, both are kept: the new
one in the copy, yours beside it in a directory named after the copy with
`.discarding` at the end, the refusal names it, and the entry is not shown
again until you move one of the two out of the way. Your
confirmation is recorded first, with what it was shown and who gave it,
and the entry stays open and listed until the copy is gone; only then is
it answered discard. If the service stops in between, the next start
puts back anything set aside and removes the copy if it still holds what
you were shown; if the copy changed meanwhile, your confirmation is withdrawn, the copy stays, and
the entry is listed again for you to look at and answer afresh. A copy
kept again at another state has an entry for each state; discarding the
copy answers and closes all of them kept by the time its removal went
ahead, also when the service stopped after the removal and the next start
finishes it; carrying it on answers and closes all of them, since the
whole copy moves to the new task. While one entry of a copy has an answer still being
carried out (a landing whose run is not started yet, a carry on or a
discard not finished), the copy's other entries are not answered, and the
task is not promoted; the answer under way finishes on its own or at the
next start. An entry whose "what the default branch lacks" is
not established (the upstream could not be read when the copy was kept) is
refused rather than shown a list that may be wrong; the next cleanup of the
unchanged copy establishes it.

A promotion of an investigation whose copy has a kept-work entry (its
worker ended with its copy kept) answers that entry carry on itself, in the
promotion's own write, since the promoted worker continues in that copy;
the entry closes with the promotion and never without it.

Discard is yours alone. The service admits a confirmation only from a
process in the foreground of its controlling terminal, the same relation a
local task's approval is held to, decided from what the kernel reports of
the caller and not from anything the caller says, so a coordinator agent, a
script, a cron job or a call over the socket from no terminal is refused
and removes nothing; the command line refuses `--confirm` under `--json` or
off a terminal before it asks the service; and a task's worker and a
subordinate coordinator are refused it too. Nothing in the program discards
on its own, and the preview is not restricted. What the record says about
who discarded is what the service witnesses, which is the machine interface
and not a person: an agent you run in a terminal's foreground can confirm
as you would, so the answer is recorded as the machine interface's and
never claims a person decided (section 8's resolver rule).

## Waiting on a fleet

A coordinator agent running tasks through `assistant coordinator` does not
need to poll the wake queue. A person can look at the queue with `assistant
coordinator wakes`: it lists the waiting wakes, oldest first, by the project
of the work each is about, and says whether the coordinator has been handed
each one. It takes nothing, so the coordinator still gets every wake it
showed.

The coordinator reads the queue with `--json`, which is a drain: `assistant
--json coordinator wakes` hands out the wakes on offer, oldest first, and
closes none of them. A wake stays waiting until the coordinator acts on it and
then acknowledges it with `assistant coordinator acknowledge`, which is the
only thing that records a wake as handled. A wake handed out and never
acknowledged is handed out again: after a service restart, and once the
coordinator it was handed to is no longer attached. The service counts the
coordinator as attached while its wake stream, `listen` below, is held, so a
coordinator that holds `listen` is not handed its own wakes twice, and one
whose `listen` ended, because it went away, gets them back on its next
drain. A coordinator that holds no stream is never attached, so every drain
hands it the wakes it has not acknowledged yet. The structured answer counts,
in `handouts`, how many times each wake has been handed out.

### Wake action contract

Delivery is at least once, so `handouts` is a delivery counter, not a licence
to act again. Act keyed by `(task_id, kind, session-ref)`:

- Read the task (`assistant --json tasks` or the task surface) before acting.
  For `terminal-gone`, the session reference sits in the wake's `marker`
  between the case (`dead`, `unobservable`, or `provider-wide`) and the
  unobserved-since instant. If the task's latest attempt already names a
  different session, or the program has already relaunched, do not call
  `task.relaunch` again.
- Acknowledge every wake id you acted on, including one you decided needs no
  further action after reading the task. An unacked row stays in flight.
- A resurface whose marker carries `:again:` is the same silence reminded
  once the escalation window elapsed. It is not a second crash and must not
  start a second relaunch when the first surface was already handled or the
  program already relaunched that session.
- A marker case of `unobservable` is ambiguous liveness: probe or wait. Never
  call `task.relaunch` on it. The service leaves Unobserved alone the same
  way; only a later `dead` reading relaunches.

The service can push wakes to the coordinator. `assistant coordinator listen`
holds the coordinator's wake stream and prints each wake as the service
queues it, until it is interrupted:

```sh
assistant --json coordinator listen
```

It hands out and records nothing: a wake it prints stays waiting until a drain
hands it out and an acknowledgement closes it, or until `assistant coordinator
acknowledge` names it. Its first line is a gap marker, and a gap later means
some pushes were missed; either way, drain the queue, which gives back every
wake still waiting that this coordinator was not already handed. It suits a
host that can run a command in the background and start a turn on its
output. It ends with success when interrupted, and as an operational failure
when the stream ends any other way, a service stop included, so it has to be
started again.

A coordinator that cannot hold a stream waits instead. `assistant coordinator
await` blocks until the queue holds a wake on offer, then drains it the way
`assistant --json coordinator wakes` does, or lists none once its bound
passes:

```sh
assistant --json coordinator await --timeout 5m
```

The bound is whole seconds from `1s` to `1h`, and `5m` when none is given;
anything else is refused as incorrect usage rather than clamped. A wake it
returned is still waiting until it is acknowledged, so the loop is: wait, act
on each wake, acknowledge it, wait again. A coordinator that waits again
before acknowledging is answered at once with the same wakes.

Delivery is at least once, and the limits are stated rather than hidden. The
service cannot see the coordinator, only its stream: a `listen` left running
after its host died keeps what it was handed until that process ends, and two
streams held at once, a person's and the coordinator's, count as one, so the
coordinator going away is not noticed until both have closed. A coordinator
that holds `listen` and never acknowledges what it acted on is not handed
those wakes again, and they keep the work counted as in flight.

`assistant coordinator acknowledge` closes the wakes it names, whether a drain
handed them out or `listen` printed them. Naming a wake already acknowledged
is refused as incorrect usage, and so is naming a number no wake has.

Neither `await` nor `listen` keeps anything between calls, so after a service
restart, which ends either as an operational failure, starting them again
reads the same durable queue; `assistant coordinator supervision` confirms
the service is watching the fleet again first. Like the other coordinator
subcommands, all of these are refused for a task's worker.

A restart does not leave that queue exactly as it was. When the service
starts it reconciles every task, and a task whose worker's terminal is gone
with its work not landed and no report filed for it is relaunched, up to
`relaunch_attempts` times (2 unless configured). A relaunch records each still-queued `terminal-gone`
wake about the session it replaced as handled by the program, so no later
drain returns it. Read `assistant tasks` after a restart as well as
the queue: a relaunched task shows its new attempt there, and a task past
`relaunch_attempts` holds for an answer instead of being relaunched.

How soon a wake is pushed or answered after a worker stops is set by the
service's supervision pass, which records the wake, not by the stream or the
wait.

## Hearing about a wake on a private channel

The coordinator is the one the wake queue wakes. If you also want to hear
about each wake yourself, somewhere other than a terminal, name a command in
your own configuration file, `config.yaml` under the home:

```yaml
notify:
  command: ~/bin/notify-me
```

The service runs that command once for each wake its supervision pass
queues, with the wake on standard input as one line of JSON, followed by
end of file. The line is the wake object itself (`id`, `task_id`, `kind`,
`marker` and the rest), not the event `assistant --json coordinator listen`
prints: a `listen` line wraps the same object as its `payload`, beside
`type` and `revision`, and the command's line has no such wrapper. A wake
about no task carries no `task_id`: one about a delegated domain names
`delegation` and `domain`, and one about kept work no task names names the
entry in `kept_work`, with its `project` and `branch`. The
command decides where it goes: a script that posts to your own private chat
channel, writes a desktop notification, or appends to a file. The line is a
line of shell, run the way `commands.test` is, in the assistant home (by
default `~/.assistant`) with the service's environment, so it has your credentials; it is given 30
seconds per wake and is ended after that, together with whatever it started
that is still in its process group (on Linux and macOS).

What is sent, and what is not:

- Each wake is offered once. The service records, for each wake, that it
  ran the command and how the command ended: `sent` when it exited with
  status 0, `failed` when it exited otherwise, could not start, ran past its
  30 seconds, or was ended by the service stopping, with the exit status and
  the end of what it printed, credentials removed. A failure is also written
  to the service log. A failed wake is not sent again, because the command
  may already have delivered it, and a wake the service was running the
  command for when it crashed is not sent again either, and its record
  shows no outcome. The same holds when the command ran but the service
  could not then record how it ended: the wake is not sent again, the
  record shows no outcome although you may have received it, and the
  service log says the record could not be written but not how the
  command ended.
- `sent` means your command exited 0. It does not mean anyone read it.
- Only a wake still waiting for the coordinator is sent. A wake the
  coordinator acknowledged before the command ran for it is not, and when
  you name a command for the first time the service sends what is still
  waiting, not the queue's whole history.
- The command changes nothing. What it prints and how it exits are recorded
  and never read as an answer, an acknowledgement or an instruction, and a
  wake it was sent stays waiting for the coordinator. Replying through the
  channel is not supported: answer a hold with `assistant --task ID --answer
  ...` as before.
- Only your own file may name the command. A repository's `assistant.json`,
  on the default branch or on a pushed branch, that names `notify.command`
  is refused when it is read, like every key only your file may set.
- The service reads the command when it starts. A change reaches the next
  service, after `assistant service restart`.
- Commands run one at a time, so a channel that hangs delays the wakes
  behind it by up to 32 seconds each on Linux and macOS: its 30 seconds,
  and at most 2 more for what it printed to arrive. It never delays
  supervision itself.
- If the service cannot record that it is about to run the command, for
  example because the disk is full, it writes that to the service log once,
  stops offering wakes, and tries again about every 5 seconds.
- With no command named, nothing runs and nothing is recorded.

### Delegating a domain

A coordinator can hand a named set of tasks to a coordinator of its own,
started by the service in this same home:

```sh
assistant coordinator delegate DOMAIN --delivery gated --instructions "what the domain is for"
assistant coordinator end-delegation DOMAIN
```

The domain is one to thirty-two lower-case letters, digits and inner dashes.
The service records the delegation, with the posture every task of the
domain takes, and starts the subordinate coordinator through the same
session provider and adapter it starts a worker through, in
`domains/DOMAIN` under the home, with the `assistant coordinator` route as
the one command its grant allows. It is placed by the process it runs in,
exactly as a worker is, so every task it creates is written into its domain
and it reaches only that domain's tasks: it holds `create`, `tasks`,
`message`, `stop`, `peek`, `relaunch`, `relayed`, `wakes`, `await`,
`acknowledge` and `supervision` over them, its `tasks` listing shows the
domain alone, and it is refused `promote`, `answer`, `listen`, `delegate`,
`end-delegation`, marking the catch-up read, starting, rerunning or
cancelling a run, `sync`, and stopping or restarting the service, with code
`subordinate`; a task outside its domain is refused with code
`outside-domain`. `tasks` and `answer` are coordinator subcommands as well
as the `assistant tasks` and `assistant --task --answer` a first
coordinator runs, because the subordinate's grant allows the
`assistant coordinator` prefix alone. A promotion or a hold's answer it
asks for is refused and recorded as a decision it handed up, which reaches
you as a `subordinate-escalated` wake and sits on the task's `--json`
answer as `escalations`.

Wakes are routed by kind: while the delegation stands, a wake about a task
in the domain that the subordinate can act on (its worker gone, idle, or
unacknowledged, among them) is handed to the subordinate's drains alone,
and a decision (a hold, an escalation) to yours alone, so no wake is handed
to both. `end-delegation` ends the delegation, its one revision, ends the
subordinate's session this service holds, and the next drain of yours is
offered the domain's rows; the tasks stay in their domain. Nothing else
ends a delegation: not a restart, which re-probes the subordinate's
session, relaunches a dead one up to `relaunch_attempts` times and places
one still running from its record and its process, so it stays its domain's
subordinate and `end-delegation` ends it; and not a dropped connection. A subordinate whose session is gone reaches you as a
`subordinate-gone` wake, once and then once per `escalation_window` while
it stays gone, and once it has been relaunched `relaunch_attempts` times
the delegation holds for you, which reaches you as a `hold-opened` wake on
the domain; both are wakes about a domain rather than a task, listed under
`domain_wakes` in the structured answers with numbers of their own, and
acknowledged as `dNUMBER`:

```sh
assistant coordinator acknowledge 12 d3        # wake 12, and domain wake 3
assistant --domain DOMAIN --answer retry        # begin its coordinator once more
assistant --domain DOMAIN --answer stop         # end the delegation instead
```

A subordinate is never handed a wake about a domain, its own included. What
this build does not do: a subordinate cannot hold `listen`, so it waits
with `await`.

## Unattended operating posture

When nobody is at the keyboard, read the fleet against these postures.
They are distinct on purpose: treating one as another is how recovery goes
wrong.

| Posture | What is true | What the service does | What you do |
|---|---|---|---|
| Active execution | Terminal Live, or a validation run the service is advancing | Absorbs; no wake | Nothing |
| Declared wait | Worker's most recent word is a declared wait, its terminal is Live or Unobserved, and its recheck window is open; the task resolves Paused | Absorbs until the recheck window elapses, then surfaces `declared-external-wait` once per window; an unchanged screen is not read as a stall | Nothing until a wake surfaces |
| Stalled screen | Terminal Live, no declared wait, and the capture is unchanged past the escalation window | Surfaces `terminal-unchanged` once, then absorbs redraws until the next window | Inspect; do not treat as a crash |
| Dead process | Probe answers Dead; work not landed; no stored report | Queues `terminal-gone` with case `dead`; on restart (or `task.relaunch`) sweeps the standing copy and relaunches in place up to `relaunch_attempts`, acknowledging that session's queued `terminal-gone` wakes as the program | After a restart, read `assistant tasks` before acting on a re-handed wake |
| Ambiguous / unobserved | Probe answers Unobserved (unheld survivor, unreadable, foreign home, and the like) | Surfaces `terminal-gone` with case `unobservable`; never relaunches; never deletes the standing copy | Probe or wait; never `task.relaunch` |
| Blocked decision | Open hold naming the task (relaunch bound, no-sweep, stop, report decision, …) | Resolves Held; leaves alone until answered | `assistant --task ID --answer …` |
| Failed recovery | Relaunch or sweep refused, or past `relaunch_attempts` | Holds or surfaces; does not destroy the copy | Read the hold or service log; answer retry/stop or fix the host |

A legitimate declared wait is never classified as a dead worker. A dead worker
is never left looking like a wait once the probe answers Dead. Ambiguous
state never triggers destructive cleanup or a duplicate delivery action: the
copy stays, and a second handout of the same wake id after a lost ack does
not start another attempt once the program has already relaunched that
session.

**Declared wait against the terminal.** A worker's declared wait outranks
its live terminal: a worker that said it is waiting resolves Paused while
its terminal runs, and its unchanged screen is not surfaced as
`terminal-unchanged` (the owner's decision on #303). It never outranks a
dead terminal: once the probe answers Dead the task resolves as the dead
process row says, whatever it declared. An unobserved terminal under a
declaration still surfaces `terminal-gone` with case `unobservable`, as
the ambiguous row says, because the declaration does not show the worker
is still there. Only the worker's most recent word counts, so a worker that
declared a wait and then reported working again is watched as running.
The residual is the recheck window itself: a worker that declared a wait
and then stalled for another reason is not surfaced until that window
(`recheck_window`, 2h unless configured) elapses.

**No worker heartbeat.** Nothing in this build sends or expects a periodic
sign of life from a worker. What the service reads of a worker is its
terminal's probe (Live, Dead or Unobserved) and its captured screen, so a
worker that stopped producing output reads as the stalled screen row, and
one whose process ended reads as the dead process row, never as a missed
heartbeat. The one periodic sign of life is the supervisor's own beacon,
which the supervisor loop rewrites every `supervision_beacon_interval` and
which the stop hook, the pull guard and the service's watchdog read as stale
at three times that interval. It says whether the supervisor is running, and
nothing about any worker.

**Residual (listen attachment).** Being attached is the `listen` process, not
your session. A zombie `listen` left running after its host died keeps
handouts pinned under that stretch until the process ends; this build does
not expire or heartbeat that attachment. Acknowledge as you go, and do not
leave a detached `listen` running.

## Stopping the service

`assistant service stop`, SIGTERM and SIGINT all stop the service the same
way: it stops taking requests, cancels the runs it is advancing, which asks
each agent it started to end, and ends its workers. That is bounded by
`stop_grace` in your own configuration file (30s unless configured, between
1s and 10m; a repository file naming it is refused). When the grace runs out
before the stop is done, the service log names the step it was still
waiting on, every process group the service registered is ended by force,
and the service exits within about a second more. A second SIGTERM or SIGINT
while it stops forces the same thing at once, and a third gets the
operating system's default handling. A forced stop exits with a failure
status and does not start a replacement after a restart; the next start
recovers whatever it left. A process that moved out of its group, and a
configured command's or notify command's children, are not among the groups
ended by force.

## Standing consent

`assistant --standing-consent` starts or attaches to this branch's run and puts
that run under standing consent, which lets the program answer the holds of
the stages before push without you: a fix round over every finding that is
not a note, and approval of the hold a round leaves once only notes remain.
`assistant --task ID --standing-consent` does the same for a task's current
run, from anywhere, without checking the task's branch out.

It never answers the push, pull request, or checks holds, so a push refusal
still waits for you. It also leaves a hold for you when a finding there is an
ask whose action the reviewer never stated, or one no fix round can resolve,
such as a test, document or lint stage with no command configured, or a
review that did not happen because the change was too large to review in one
piece (`review-change-too-large`) or the reviewer came back with nothing
usable (`review-not-established`); the service log says which finding and
why. The three-round limit and the run
budget still bound the rounds it asks for, and so does the convergence bound,
which a round you ask for yourself is exempt from: a run whose consented
rounds leave nothing changed parks rather than waiting for you.

The repository has to allow it with `"allow_standing_consent": true` in
`assistant.json` on the default branch; without that the flag is refused as
incorrect usage, and a branch cannot set the key for itself. Setting the key
in your own configuration file, `config.yaml` under the home, allows it in
every repository the home serves. That is allowed on purpose, as a choice you
make for all of them at once; the flag is still needed per run. Each answer
is recorded as the program's, and status, the run listing, and the pull
request body say the run ran under standing consent.

## Bounding what one run spends

`run_usage_budget.output_tokens` in your own configuration file, `config.yaml`
under the home, caps how many output tokens one run's agent calls may report
generating:

```yaml
run_usage_budget:
  output_tokens: 200000
```

Zero, the default, is no budget. Only your own file may set it: a repository's
`assistant.json` naming the key, on the default branch or on the pushed
branch, refuses the run before anything launches. It counts output tokens and
not the prompt counts, because the agents report a prompt across input and
cache counts on their own terms, and output tokens are the one count that
names what each call generated.

Every run records the budget it starts under. Before each step the run is
compared against what its own calls have reported so far, read from the agent
invocation record that `assistant runs --usage` also reads. A run at or past
its budget stops there: it is not held for you, it is parked, its record ends,
and its answer says the budget and what the run's calls reported
(`record.park` in `--json`, with `bound` `usage_budget`, `usage_budget`,
`spent`, and `unreported`). A run whose calls include one that reported no
output token count is parked the same way, because what it spent is not
known; set a budget only on a count your agents report. To go on, raise the
budget, run `assistant service restart` so the service reads it, and start a
fresh run: the service reads your file when it opens, so a run started before
the restart is held to the old budget.

What it does not do:

- It bounds starts, not spend. A step that starts with room runs to its end,
  so a run can pass its budget by everything its last step spent.
- It counts only the calls the service makes for a run's stages and fix
  rounds. A task's worker and the decision-model checks are not in it.
- It is read when the service opens. A run started under another value is
  refused when it is next resumed rather than quietly held to either number.

It is not `usage_ceiling`. The ceiling counts every run's calls together over
a window, refuses the next call once it is reached, and holds that run's stage
for you; once there is room, `assistant --answer retry` runs that stage again
in the same run. The budget is one run's, counts that run alone from its first call,
and parks the run.

## Watch a worker in a terminal

By default a worker runs on pipes the service holds, which nothing can attach
to. To host each worker in a terminal you can watch and type into, set the
provider in your own configuration file, `config.yaml` under the home (by
default `~/.assistant`), which is a YAML document:

```yaml
session_provider: tmux
```

Workers also need an adapter that declares terminal workers (today: Claude).
A Cursor-only `agent:` pin refuses the launch with a recorded `route-refused`
reason rather than falling back or widening grants; list Claude after Cursor
if you want Cursor for runs and Claude for attachable workers. See
`docs/multi-agent-support.md`.

`session_provider: herdr` and the keys for workers on another machine
(`remote_hosts`, `placement.local`, `placement.arch` and
`remote_disconnect_grace`, PRD section 8) are parsed and validated, but this
build does not act on them yet. The service refuses to start on a
`config.yaml` that sets any of them, naming the key, rather than running
workers some other way. A repository whose default branch's `assistant.json`
sets `placement.arch` is refused the same way, naming the key, wherever the
service reads that copy: a run, a delivery task's intake or promotion, and
standing consent. A task of such a repository that reaches dispatch, an
investigation or a delivery task taken before the key was added, is not
dispatched: it stays queued, and the next dispatch pass after the key is
removed dispatches it. Meanwhile the coordinator is told about it as it is
about any task with no worker to watch: on the supervisor's second pass over
it (about one `supervision_beacon_interval` in) and again each `escalation_window`,
as a worker whose screen has not changed. Only the service log says the wait
is placement.

Each launch records which adapter it chose on the task attempt in the home's
database, once, as `adapter`, `route_reason` and `route_skipped`; no command
prints the attempt yet. The reason is `override`, `ordered`, `fallback-unavailable` or
`fallback-quota`. Each skipped entry names its adapter and one of
`names-no-agent`, `not-in-build`, `cannot-run` or `missing-capability`, with
the missing capability beside the last. A run records its own agent the same
way, as `resolved_agent` and `resolved_skipped` in the run's record, which
`assistant --json runs RUN` prints.

It is read strictly: an unrecognized key, a key written twice, and a value of
the wrong type are each refused by name rather than ignored or converted, so
`yes` is not `true` and `"3"` is not `3`. A file written as JSON, as this one
used to be, still reads the same, except that the escape `\/` is refused; write
`/` instead. A file that does not parse stops the service from
starting; `assistant service start` and `assistant service restart` print why,
and a restart leaves the running service up rather than replacing it with one
that cannot start.

Only that file may set it: a repository's `assistant.json` naming it is
refused, trusted copy or pushed, because the provider decides which program
the service runs to start every worker. The service reads it when it first
resolves an agent, so restart the service after changing it.

Each worker then runs as the one process of a tmux server of its own, on a
socket under the home's `terminals` directory, started with no tmux
configuration file. `assistant --json coordinator peek ID` reads the worker's
recent output as a transcript, redacted and bounded to 16 KiB, and its `attach` field is
the command that attaches you:

```sh
tmux -S ~/.assistant/terminals/SOCKET attach-session -t worker
```

What you type there is the worker's input, exactly as a message is, and the
program keeps no record of it: the worker acts on it, and the coordinator
reconciles around what the worker then does. A command the worker runs
because you typed it is the worker's, so its pushes are held to the worker's
own branch like any other. So is a new window you open in that server: the
server is the worker's, every window on it is placed as the worker, its
pushes are held to the worker's branch, and the coordinator's commands are
refused there. The worker can open a window on its own server too, since
tmux names the socket in its environment, and that window is the worker's
for the same reason; a push the worker makes that way is held to its branch
rather than admitted as yours. To push or coordinate as yourself, use a
shell of your own, not the worker's server.

Two things it needs. tmux has to be on the service's PATH; without it every
launch is refused and the task stays dispatched, which the service log says.
And the resolved agent has to declare terminal workers, which Claude Code
does. `assistant doctor` checks both when this file chooses tmux, and reports
the stall by name before a task waits Dispatched. Cursor and Grok do not
declare terminal workers; naming one under tmux fails that check the same
way. A worker an earlier service started is never attached to or read
through the new one: its terminal reads as unobservable until it ends, as it
does under the default provider.

A Claude Code worker under tmux runs the CLI's interactive interface rather
than its pipe mode, and reaches its conversation without anyone answering
the CLI's first-run screens: each worker gets a configuration directory of
its own, in the system's temporary directory, that records onboarding as
done, trusts the worker's copy, and approves its API key when it runs on
one, and the directory is removed when the worker ends. Your own `~/.claude.json` is not read or
written; whether the CLI still reads anything else under your home, such as
your settings, has not been established. That directory carries none of your
login, so the service needs a credential in its environment: either
`ANTHROPIC_API_KEY`, an API key, or `CLAUDE_CODE_OAUTH_TOKEN`, the
long-lived token `claude setup-token` prints for a Pro or Max subscription.
If both are set the API key wins and the token is removed from the worker's
environment; each worker is handed exactly one. Without either, every
Claude Code launch under tmux is refused with an error naming both
variables. On the API key, the directory holds the key's last 20
characters, which is how the CLI records an approval, and never the whole
key; on the token, it holds no part of it. Neither value is recorded
anywhere by this program, but both reach the worker's tmux server and the
CLI through their environment, as any variable the service passes does.
Only presence is checked at start: a key or token the account no longer
accepts is not detected before the worker launches.

Cursor and Grok are different: those adapters strip `CURSOR_API_KEY` and
`XAI_API_KEY` and authenticate only through their CLI logins
(`cursor-agent login`, `grok login`). Name them in the `agent` key when you
want them; `auto` still selects only Claude. See
`docs/multi-agent-support.md`.

What you see in the pane is the CLI's flat screen reader layout. A message
the program sends waits until the worker's current turn has ended and its
input is empty, and then arrives as one paste, so a message of several lines
stays one message. The service waits 10 seconds for that, so a message sent
during a longer turn is refused, with nothing typed, and the message record
says so; send it again once the turn has ended. A message beginning with `!` or `/` is sent with a
space in front, so it reaches the worker as a message rather than running as
a shell command or a slash command. Some limits are worth knowing:

- What you type in the pane is not given that space. Typing `!` into an
  empty input there runs a shell command outside the worker's permissions,
  as it does in any Claude Code session. That residual is not closed: a
  person attached to the pane can still reach a shell the program did not
  grant. A command run that way descends from the pane, so a push it makes
  is placed as the worker's on the same terms as any command the worker
  runs; what it escapes is the CLI's grant, not placement. Do not take
  attach as a guarantee that every keystroke stays inside the worker's
  permissions.
- A worker that starts a second tmux server, on a socket of its own, puts
  what runs there outside its process tree, and a push from a window of that
  server is placed as a shell's, because the second server gives its windows
  terminals of their own, and starts the full run. The worker's own server
  is covered; a server it starts is not. Any other daemon a worker starts
  has no terminal, and a push from there is refused with code `unplaceable`.
- A long or multi-line message reaches the model wrapped as pasted content
  rather than as bare text.
- All of this rests on what Claude Code 2.1.301 drew on screen and kept in
  its state file. A later version that draws something else has its workers
  refused at start, or its messages refused, rather than typed at blind; one
  that draws the same and behaves differently is not detected.
- Instructions longer than about 16 KB do not fit the command that starts a
  tmux worker and are refused.

## Keep your CI required

The gate is not your CI (PRD section 12 lists "Being your CI" as a permanent
non-goal). Keep the forge's required checks on the pull request as the shared
outer check. The last stage reads the checks registered on the pull request's
head. While checks are still running, or none are registered yet, the run waits
rather than holding: the stage re-reads the checks about once a minute and
reports what they settle on. `checks_timeout` (global-only, default `168h`)
bounds that wait, and when it elapses the run holds for you rather than ending.
The timeout starts when the stage first finds the checks unsettled, starts again
whenever your base branch advances on the upstream, and is kept across a service
restart, so resuming a run does not reset it. A pass does not say the checks
stayed green, and it is not a merge decision.

While the stage waits, the run is not held, so there is nothing to answer. A
run's outcome reads `executing (a segment is running)` for as long as the wait
lasts: up to `checks_timeout`, and longer if your base branch keeps advancing.
The `assistant` command that started or attached to the run blocks for that
whole time. Walking away from it (interrupting it, closing the terminal) does
not stop the wait: the service carries the run on by itself and keeps reading
the checks once a minute with nobody attached, because the wait belongs to the
service rather than to the command that started it. To stop waiting, run
`assistant --cancel` on the branch, which ends the run. A run that a newer push
superseded can also keep reading the checks until they settle or the timeout
elapses, when the service had not yet registered it at the moment of the push.

Every read of the wait reads the pull request itself before the checks, by the
number the pull request stage recorded. A pull request the code host reports as
merged ends the wait and the run: the run reads `passed`, and its checks stage
report says the checks were not read to a verdict and that the `no_ci` rule was
not applied, because a merge completes the run without validating it. A pull
request reported as closed without merging ends the run as `failed`, with the
reason `pull request closed without merging`. An open pull request, one in a
state this build does not recognize, and one the host would not describe all
keep the run waiting.

The hold the elapsed timeout leaves is re-read the same way before it is
resumed: at service startup, and again when you answer it. A pull request found
merged or closed by then closes that hold with a resolution the service records
as its own, naming the state it read, and ends the run as above; the answer you
gave is not applied, and the command reports the run as it ended. No other hold
is re-read. A hold on your decision about a finding is yours to answer, whatever
the pull request's state.

If a check or base read fails outright, the stage stops instead of waiting
through the failure, and the run's outcome reads `executing (stalled - nothing
is advancing this run)`. Running `assistant` on the branch again resumes it into
the same wait, and the first read of that resumed wait reads the pull request
like every other.

A repository with no CI at all must say so by setting `no_ci` in its trusted
configuration. Without that declaration an empty list of checks is not treated
as a pass.

The same read of the pull request carries whether the code host can merge it.
Checks that passed are not a pass until the host also reports the pull request
mergeable; until then the run waits on the same terms, and an elapsed wait over
passed checks holds with `ci-mergeability-unknown`, which is re-read like the
other elapsed holds.

A pull request the host reports as conflicting with its base is answered at
once, whatever its checks say, by sending the run back to the rebase stage
(PRD section 14, decision 7). The rebase stage observes the branch and goes on
only if it still stands at the commit this run pushed, then replays the run's
head onto the freshly fetched base. If that applies cleanly, the result goes
through review again, in a session of its own, then the test, document and
lint stages, and a second push anchored on the commit the first push left
there, before the checks stage reads the pull request again. The run takes
this path at most twice (`RebaseAfterPushRounds`); a third conflict parks the
run rather than rebasing again.

The run never decides on its own what conflicting content should be. A rebase
that does not apply cleanly is abandoned and holds with
`rebase-after-push-conflict`, with nothing rewritten: rebase the branch yourself
and push it through the gate, which starts a new run that reviews the result. A
fix round you ask for at that hold is your explicit answer, and the rebase stage
then rebases its result only if that applies cleanly. The second pass also
holds instead of rebasing when the branch moved after this run pushed it
(`rebase-after-push-branch-moved`), so a commit somebody else pushed there is
never dropped, and the second push is refused and held like any other when the
branch moves after the rebase stage observed it. A rebase that leaves nothing
to push holds too, rather than ending the run past an open pull request.

The checks stage holds with `ci-conflicted`, as before, where that path cannot
do its job: the run pushed nothing, its head is not the commit it pushed, or
the run skips the rebase, review or push stage. The fix round limits of the
stages the path passes through are per run, so a stage that spent its rounds
on the first pass has none left on the second.

A failing check is sent to the checks stage's fix rounds
(`fix_rounds.checks`). The round commits a repair in the run's isolated copy,
and the run takes it back through review before it reaches the pull request:
the checks stage observes the branch and goes on only if it still stands at
the commit this run pushed, the review stage reviews the repaired head in a
fresh session, the test, document and lint stages run again, and the push
stage pushes it anchored on that commit, before the checks stage reads the
checks again on what it pushed. A branch somebody else moved since this run
pushed it holds with `ci-fix-branch-moved` and nothing is pushed, and a branch
that moves after that observation is refused at the push and held, so a commit
somebody else pushed there is never dropped. Fix rounds are per run, so
`fix_rounds.checks` bounds the repairs: a check still failing after the last
round it allows parks the run in front of the checks stage's fixer. The run's
answer says whether a repair was made and whether it has been added to the
pull request since.

Where that path cannot do its job, because the run pushed nothing or skips the
review or push stage, the stage holds with `ci-fix-not-pushed`, naming the
round's commit, the commit the pull request still stands on, and why, instead
of reading checks that could not have seen the fix.

The path changes the stage graph's edge digest whenever `fix_rounds.checks` is
above zero, so a run checkpointed by an earlier build is refused on resume and
has to be started again.

## Keep `commands.test` targeted

`commands.test` is empty by default. When you set it, the test stage runs that
one command against the run's isolated copy and passes if it exits zero. The
stage derives nothing, so the targeting is whatever the command does. Point it
at a check for the change, such as one package or one affected suite, and leave
the full suite to CI. With no command configured the stage holds for a person
rather than reporting a pass it did not establish.

The command executes shell, so it is read from the operator's own
configuration (`config.yaml` in the home) or from `assistant.json` on the
default branch, never from the branch under validation, unless the operator has
set `allow_pushed_commands`. That opt-out cannot be set from a pushed branch.

The test, lint and document commands are started by the service, on the host
it runs on, with your credentials and at the service's own scheduling
priority, so a full-repository command can use every core. Three bounds apply
to each one:

- It may run for `command_timeout` (30m unless configured, between 1m and
  24h, set only in your own configuration file; a repository file naming it
  is refused). Past that it is ended and the stage holds for you naming the
  time limit. Raise it when a repository's targeted check legitimately runs
  longer.
- On macOS and Linux it runs in a process group of its own, and the group is
  ended when the command returns or reaches its limit, so a child it started
  does not outlive it. A child that moved out of the group (`setsid`, a
  daemonizing tool) is not ended.
- The service runs at most a quarter of the host's logical CPUs' worth of
  these commands at once (at least one); a run whose command finds no free
  slot waits, and reads as running while it does. Nothing lowers a command's
  priority, so one command can still saturate the host by itself: keep the
  commands targeted.

## Bounding git and gh

Every git and `gh` the service runs is ended with its process group when it
runs too long, so a wedged fetch or a hung code-host request cannot hold a run
forever. Two keys in your own configuration file set those bounds:

| Key | Default | Accepted | Bounds |
| --- | --- | --- | --- |
| `git_deadline` | `10m` | `1m` to `2h` | one git invocation: a clone, a fetch, a push, a read |
| `gh_deadline` | `2m` | `10s` to `30m` | one `gh` invocation against GitHub |

```yaml
# config.yaml in the home
git_deadline: 30m
```

- Raise `git_deadline` when a large repository's clone or fetch over a slow
  link needs longer than ten minutes; lower either for a tighter bound.
- A value outside its range refuses the whole file when the service starts,
  naming the key. Both are global-only: a repository file naming either is
  refused, so a branch cannot loosen a bound that protects your machine.
- They apply where the code calling git or `gh` set no deadline of its own;
  where it did, that deadline is kept instead.
- Stage commands keep their own `command_timeout` (above), and no other
  program's bound is configurable.
- The command line's own git and `gh` (`assistant init`, `doctor`, a push's
  receive into the gate) do not read your file and run under the defaults.
- The GitLab adapter talks HTTP and starts no program, so `gh_deadline` does
  not bound it.

## Repositories with no code host

A repository whose upstream is a local path or a `file://` remote has no code
host to open a pull request on. A run of one still walks the gate through
push; the pull request and checks stages are skipped with that reason on the
run (`no_code_host`), and the run ends `pushed` rather than `checks-passed`
(checks that did not run are never claimed). Merge the change on the remote
yourself. `assistant doctor` says so up front on its upstream row.

A remote that names a network host this build does not address (a self-managed
GitLab, GitHub Enterprise, an scp-style host that is neither github.com nor
gitlab.com) is refused when a run starts, with a typed error, rather than
walked eight stages and stalled.

## Repositories on GitLab

A repository whose recorded upstream is a project on gitlab.com is validated
the same way and proposed as a merge request there. The service talks to
GitLab's REST API directly, not through `glab`, and authenticates with an
access token it reads once, when it starts, from `GITLAB_TOKEN` in its own
environment; a configuration file cannot supply one. Without a token every
request to GitLab is refused before it is sent, so a run reaches the pull
request stage and stops there saying so. `assistant doctor` reports on its
code-host row whether the token is set in the environment you run doctor in,
which is not necessarily the one the service was started in.

What differs from GitHub, each in the stricter direction:

- The checks are the merge request's pipelines on its head commit. A pipeline
  GitLab reports `skipped` ran nothing and is not counted, so a head whose
  only pipeline was skipped has no checks, which is not a pass without
  `no_ci`. A pipeline waiting on a manual job keeps the run waiting. A
  pipeline a person replaced with a newer one on the same commit is still
  read, so a failed one keeps the checks failed until the head moves.
- The commit status list is not read, because GitLab lists a pipeline's own
  jobs there, including jobs allowed to fail. A status an external system
  posts that appears in no pipeline on the merge request is therefore not
  read.
- Whether the merge request can be merged is read from GitLab's conflict and
  merge-status fields, not its detailed status, which folds in pipelines and
  approvals.
- A merge the program asks for under `autonomy.merge` sends GitLab the
  merge request's head commit and nothing else: no merge method, no squash
  choice, and no merge when the pipeline succeeds, so the project's own
  settings decide how it merges, and a commit pushed after the run's read is
  not merged.

A fork on gitlab.com is supported beside an upstream on gitlab.com; `assistant
init` refuses a fork on one host beside an upstream on the other.

## Merge order and stacking

The gate opens or updates one pull request per branch. It merges one only in a
repository the operator's global-only `autonomy.merge` lists, after the live
read PRD section 6 gives, bound to the head the run observed. The list names a
GitHub repository as `owner/name` and a GitLab project with its host in front,
`gitlab.com/group/project`, which is how the program names a GitLab run's
repository; the two forms never match each other, so `group/project` alone
never lets the program merge on GitLab:

```json
{"autonomy": {"merge": ["owner/name", "gitlab.com/group/project"]}}
```

A merge the code host refuses (on GitLab, for example, the 405, 409 or 422 its
API documents for a merge request it will not merge, whose source branch moved
past the head the run observed, or that it did not merge) is written to the
service log and is not asked again. The log line carries the host's own status
and message, except that on GitLab an answer larger than the output bound is
reported as oversize with neither, and a redirect with its status alone. The run stays
`checks-passed` for you to merge. Its outcome vocabulary has `passed` for a merged pull request,
and otherwise what produces it is a pull request someone merged while the
checks stage waited on it, or while the hold that wait left was standing, or
after the run ended `checks-passed`. Merge order, stacked pull
requests, and queueing belong to the forge or to merge-queue tooling you already
use, and the binary has no stack view. If you stack branches, each is pushed and
validated as its own run, and the pull request stage reports which base the run
validated against so a reviewer can see when that differs from where the pull
request merges.

## What a pass does not cover

Review reads your change before the test, lint, and checks stages run. When a
fix round commits code after review, either an automatic one at the test or lint
stage or one you ask for at the test, document, or lint hold, the push stage reviews the
commits between the reviewed one and the head before it pushes anything, and
holds unless that review raises nothing but notes (PRD section 14, decision 7).
Two things that review does not cover:

- The checks stage's fix rounds. That stage runs after the push, so the push
  stage's review of later commits never sees what its rounds make. They are
  reviewed instead by the review stage the checks stage sends a repair back
  to before the second push, as described above, and so is a rebase the
  checks stage sends the run back for. Where the run cannot take a repair
  back, the stage holds with `ci-fix-not-pushed` rather than reporting on
  checks that never saw the round's commit.
- Earlier rounds' own accounts. The reviewer of the later commits is handed
  each stage's last fix summary as a claim to check. A stage that took more
  than one round is represented by its last summary, and its earlier rounds
  only by the diff.

## Hosted reviewers

Hosted review bots such as Copilot or CodeRabbit can comment on the pull request
the gate opens. They complement the gate and do not replace it. They run
outside the gate, so the properties the gate is built to hold do not extend to
them:

- P4: reviewing and fixing are separate roles, and a review never resumes the
  session that prescribed the fix it checks.
- P7: configuration that executes code is read from the default branch, never
  from the pushed commit.
- The judge-path rule: a fix round after review that touches a test, CI, or lint
  file holds for a person (`judge_paths`, PRD section 10).

Nothing in this build reads or answers those bots' comments, so treat them as
input for the human who reviews the pull request. The tests for P4, P7 and the
judge-path rule show the machinery behaves as specified, not that every review
is good; `internal/journey/README.md` says what a green run there establishes.
