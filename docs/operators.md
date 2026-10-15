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
  reviews the commits its rounds make, and nothing in this build pushes them.
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
