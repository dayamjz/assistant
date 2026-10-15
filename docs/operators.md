# Operating the gate

This is a short guide for the person or agent that pushes work through the gate
and for whoever owns the repository's CI and merge settings. It describes what
this build does today. [`prd.html`](prd.html) is the specification, and where
this page and the code could disagree, the code and the PRD win.

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
branch to learn, so give `--default-branch` beside it.

The test and lint stages run the commands the trusted configuration names and
hold for you when it names none. To shorten that setup, `init` reads the
checkout's files for the commands the repository already declares (a
Makefile's `test`, `check` and `lint` targets, `go.mod`, `package.json`
scripts, `Cargo.toml`, `tox.ini` and `pyproject.toml`), runs none of them, and
offers them. At a terminal it asks about each one; elsewhere it only prints
them, and `--accept-commands` writes them all. They are merged into the
checkout's `assistant.json` and never replace a key it already names. Runs read
commands from that file only as the default branch on origin holds it, so
commit the file and land it there before expecting a run to use them. Nothing
is offered for `commands.document`: no common build file declares a command
that updates prose, so that stage keeps holding until you configure one or
answer it.

The gate's admission hook decides whether the push may proceed, and its
notification hook starts a run for that branch and commit. A push that deletes
a branch, or carries anything that is not a branch, starts nothing. An ordinary
`git push` to your other remotes is untouched, so a branch that is never pushed
to `assistant` is never validated. Nothing here stops a person or agent from
pushing elsewhere; the gate is the inner check for work that is handed to it,
not a barrier around the repository. A forge-side branch protection rule is what
enforces that for everyone.

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

The stage runs again after the round and holds again, so you see the result
before anything is pushed. A round that changed nothing says so and returns to
the hold. At most three rounds can be asked for at one stage in one run. The
push, pull request, and checks holds do not offer a fix round: push forwards a
commit, so a round there would be pushed before you saw it, and a round after
push would leave a commit nothing pushes.

A push hold whose finding is `push-refused` with reason `would-discard` means
the branch moved after the run observed it and now holds commits the run's
commit does not contain. The finding names those commits, counting rather
than listing any past a fixed bound, and states the remedy: observe the branch
again, rebase onto the commit that observation found, and resubmit anchored to
that new observation. Nothing in this build follows that remedy for you inside
the held run; approving the hold advances it without pushing.

The hold lists only the findings that are holding the stage. `--select` takes
the identifier of any finding the stage reported, including a fix or note
finding the hold does not list. `assistant --json status` lists every one, with
its identifier, under `active_run.stages[].report.findings`.

## How a task's worker reports back

A task's worker tells the program about its work with one command,
`assistant worker`, run from its own copy. The service starts every worker
with that command's full prefix: this binary's absolute path, `--home`
naming the service's home, and `worker`. That prefix is what the Claude
Code adapter's worker is allowed to run besides git without asking, and what
its instructions tell it to type. Through it a worker acknowledges a
coordinator's message by the number it arrived under, files an
investigation's report once on standard input, and records how its work
stands.

Three things follow for whoever runs the service:

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
  `assistant --task ID --answer ...`. The investigation's copy is removed
  only after its worker has ended, which it does when it exits on its own or
  when the coordinator stops it, and only on the cleanup check's proof. A
  report filed by a worker that is still running keeps its copy.
- Once its report is stored, an investigation reads `reported` whether its
  worker is still running or has ended, and `held` only while a decision the
  report raised is open. A worker that ends after filing is not a crash: a
  restart does not relaunch the task, and supervision does not queue a
  `terminal-gone` wake for it. An investigation that ends without filing a
  report still reads `unknown` and is still reported and relaunched as a
  worker that died.

## Waiting on a fleet

A coordinator agent running tasks through `assistant coordinator` does not
need to poll the wake queue. Reading the queue is a drain: `assistant
coordinator wakes` lists the queued wakes, oldest first, and records each one
it lists as handled, so the next read does not list it again. Read it with
`--json`, because the structured answer is the only place a drained wake's
task and kind are shown.

The service can push wakes to the coordinator. `assistant coordinator listen`
holds the coordinator's wake stream and prints each wake as the service
queues it, until it is interrupted:

```sh
assistant --json coordinator listen
```

It records nothing as handled: a wake it prints stays queued until a drain
takes it or `assistant coordinator acknowledge` names it. Its first line is a
gap marker, and a gap later means some pushes were missed; either way, drain
the queue, which gives back every wake still queued. It suits a host that can
run a command in the background and start a turn on its output. It ends with
success when interrupted, and as an operational failure when the stream ends
any other way, a service stop included, so it has to be started again.

A coordinator that cannot hold a stream waits instead. `assistant coordinator
await` blocks until the queue holds a wake nobody has acknowledged, then
drains it the way `assistant coordinator wakes` does, or lists none once its
bound passes:

```sh
assistant --json coordinator await --timeout 5m
```

The bound is whole seconds from `1s` to `1h`, and `5m` when none is given;
anything else is refused as incorrect usage rather than clamped. A wake it
returned is not returned again. That is the specification's "draining
acknowledges a row", and its cost is stated rather than hidden: a coordinator
that drains a wake and then stops before acting on it does not get it back
from the queue. The row is kept in the record, and the task's own state, which
`assistant tasks` reads, still shows what happened to the work.

`assistant coordinator acknowledge` records as handled a wake the coordinator
dealt with without draining it, one `listen` printed. Naming a wake a drain
already took is refused as incorrect usage, because the drain was its
acknowledgement.

Neither `await` nor `listen` keeps anything between calls, so after a service
restart, which ends either as an operational failure, starting them again
reads the same durable queue; `assistant coordinator supervision` confirms
the service is watching the fleet again first. Like the other coordinator
subcommands, all of these are refused for a task's worker.

A restart does not leave that queue exactly as it was. When the service
starts it reconciles every task, and a task whose worker's terminal is gone
with its work not landed and no report filed for it is relaunched, up to
`relaunch_attempts` times (2 unless configured). A relaunch records each still-queued `terminal-gone`
wake about the session it replaced as handled by the program, so the next
drain does not return it. Read `assistant tasks` after a restart as well as
the queue: a relaunched task shows its new attempt there, and a task past
`relaunch_attempts` holds for an answer instead of being relaunched.

How soon a wake is pushed or answered after a worker stops is set by the
service's supervision pass, which records the wake, not by the stream or the
wait.

## Standing consent

`assistant --standing-consent` starts or attaches to this branch's run and puts
that run under standing consent, which lets the program answer the holds of
the stages before push without you: a fix round over every finding that is
not a note, and approval of the hold a round leaves once only notes remain.
It never answers the push, pull request, or checks holds, so a push refusal
still waits for you, and the three-round limit and the run budget still bound
it. The repository has to allow it with `"allow_standing_consent": true` in
`assistant.json` on the default branch; without that the flag is refused as
incorrect usage, and a branch cannot set the key for itself. Each answer is
recorded as the program's, and status, the run listing, and the pull request
body say the run ran under standing consent.

## Watch a worker in a terminal

By default a worker runs on pipes the service holds, which nothing can attach
to. To host each worker in a terminal you can watch and type into, set the
provider in your own configuration file, `config.yaml` under the home (by
default `~/.assistant`), which is a YAML document:

```yaml
session_provider: tmux
```

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
recent output, redacted and cut to the last 16 KiB, and its `attach` field is
the command that attaches you:

```sh
tmux -S ~/.assistant/terminals/SOCKET attach-session -t worker
```

What you type there is the worker's input, exactly as a message is, and the
program keeps no record of it: the worker acts on it, and the coordinator
reconciles around what the worker then does. A command the worker runs
because you typed it is the worker's, so its pushes are held to the worker's
own branch like any other. A new window you open in that server is not the
worker: its pushes are a person's and start the full run.

Two things it needs. tmux has to be on the service's PATH; without it every
launch is refused and the task stays dispatched, which the service log says.
And the resolved agent has to declare terminal workers, which in this build
only the scripted adapter the tests use does, so a service resolved to Claude
Code launches no worker under this setting. A worker an earlier service
started is never attached to or read through the new one: its terminal reads
as unobservable until it ends, as it does under the default provider.

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

A failing check is still sent to the checks stage's fix rounds
(`fix_rounds.checks`). The round commits in the run's isolated copy, but that
commit is not reviewed and nothing in this build pushes it, so the next time
the stage runs it holds with `ci-fix-not-pushed`, naming the round's commit and
the commit the pull request still stands on, instead of reading checks that
could not have seen the fix.

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

## Merge order and stacking

The gate opens or updates one pull request per branch and never merges it. This
build has no merge step: its outcome vocabulary has `passed` for a merged pull
request, and what produces it is a pull request someone merged while the checks
stage waited on it, or while the hold that wait left was standing. Merge order, stacked pull
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

- The checks stage's fix rounds. That stage runs after the push, so nothing
  reviews the commits its rounds make, and nothing in this build pushes them;
  the stage holds with `ci-fix-not-pushed` rather than reporting on checks
  that never saw the round's commit. A rebase the checks stage sends the run
  back for is not one of these: it is reviewed by the review stage before the
  second push, as described above.
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
