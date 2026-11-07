# Running the gate on this repository, in shadow

This is a runbook for validating this repository's own pull requests with the
gate while nothing the gate does reaches this repository. It is written to be
followed from nothing, and what it assumes you have is:

- a Go toolchain and `git`;
- `gh`, signed in to an account that can create a private repository;
- **a coding agent this build can drive, installed and authenticated.** The
  review and fix stages launch one, and a run cannot start without it:
  `assistant doctor` reports the agent row as blocking and answers that no run
  can start. The default `auto` resolves Claude Code and nothing else, because
  Claude is the only adapter declaring itself auto-eligible, so **any other
  adapter on its own** leaves that row blocking until you name it with `agent`
  in the home's `config.yaml`. Step 5 below runs `assistant doctor`, which is
  where you read that row.

**Shadow** here means one thing only: the gate's recorded upstream is a private
mirror repository rather than this one. Every stage still runs for real,
including the push, the pull request and the checks stage, and the pull requests
the gate opens are opened on the mirror. This build has no shadow mode of its
own, because the push stage always forwards; the mirror is what makes the
forwarding harmless.

Read [Operating the gate](operators.md) for what each command means. This page
is the order to run them in, and the places this repository is its own special
case.

## What you set up, and why each piece is separate

| Piece | Why it is its own |
| --- | --- |
| A clone of its own | `init` adds a remote named `assistant` to the working copy it is run in, and records that path as the repository. Running it in a checkout you work in makes your own pushes and the gate's share a remote list. |
| A home of its own | The home holds the database, the gate repositories, the run copies and the service socket. A second home keeps the dogfood's runs, tasks and holds out of the one you use for real work. |
| A private mirror as the upstream | The push stage always forwards. Pointing it at a mirror is what keeps a dogfood run from pushing a branch, opening a pull request or merging on the real repository. |

## 1. Clone the repository

The clone comes first, because every later step runs the binary built inside
it. A binary built in the checkout you were already standing in is not on the
path any of them use.

```sh
git clone https://github.com/dayamjz/assistant.git assistant-dogfood
cd assistant-dogfood
```

Leave `origin` as it is. `init` is told the upstream explicitly in step 5, so
`origin` is only where the clone fetches from and is never a run's push target.

## 2. Build the binary, in the clone

```sh
make build        # writes bin/assistant in the clone
./bin/assistant --version
```

The gate under test is then the build you are reading. Every command below is
that binary, run from the clone's root as `./bin/assistant`. Its absolute path
is written into the gate's hooks and into the service definition, so the clone
has to stay where it is for as long as the gate does.

## 3. Give it a home of its own

The home root is `ASSISTANT_HOME`, or the `--home PATH` global flag, which is
accepted before or after the verb. Prefer the flag in a runbook: an exported
variable is lost between shells, and a command that silently acts on the default
home is the one mistake this setup exists to avoid.

```sh
HOME_ROOT=~/.assistant-dogfood
```

Nothing has to exist there yet. `init` creates the root and the directories
under it, and creating the root yourself first, which step 4 does when it
writes a configuration key, changes nothing about what `init` then does.

## 4. Create the mirror and seed its default branch

```sh
gh repo create dayamjz/assistant-gate-mirror --private
git remote add mirror https://github.com/dayamjz/assistant-gate-mirror.git
git push mirror main:main
```

The mirror needs the default branch before a run reaches the pull request stage,
because that is the base the gate opens against.

**Push `main` to the mirror again before each run, or the diffs grow.** The gate
rebases a pushed branch onto the recorded upstream's default branch and reviews
the result, so the change it reads is the difference from the **mirror's**
`main`, not from this repository's. Once this repository's `main` has moved on,
a branch containing those landings carries them into the diff the gate reviews:
the rebase hits conflicts that are not the branch's, the review reads a change
far larger than the pull request, and the scope lens, if you supply an intent,
reports every landed file as something the intent does not account for. Keeping
the mirror current is one command:

```sh
git fetch origin main && git push mirror origin/main:main
```

**Decide about the mirror's Actions before the first run, and decide it with
the checks stage in mind.** The workflows come with the clone, and `ci.yml` and
`repeat.yml` both trigger on every pull request, so a pull request the gate
opens on the mirror starts the full matrix there. `stress.yml`'s own comment is
what this tree says about the cost: hosted minutes are unbilled **because this
repository is public**. A private mirror is not, so check what a private
repository's minutes cost you, and what a macOS minute costs against a Linux
one, against GitHub's own billing documentation rather than against this page.
`ci.yml` bounds its `check` job, which runs on both platforms, at 50 minutes,
so one pull request's matrix is not small.

Leaving Actions on is what gives the checks stage something to settle on, and
is the only option that validates against this repository's real checks. It
does not work on a fresh mirror for a head that changes code, and the reason is
worth knowing before you choose:

- `ci.yml`'s **Prior check probe** step runs on `ubuntu-latest` for a pull
  request whose diff is not docs-only. It calls
  `scripts/prior-check.py --probe`, which asks the repository for its most
  recent **successful `pull_request` run of `ci.yml`**. A fresh mirror has none,
  so the script fails with `no successful pull_request CI run to probe against`
  and returns 1, deliberately: its comment says an unreadable API "fails loudly
  so the step cannot go green without checking either path". That fails
  `check (ubuntu-latest)`, which is one of the five checks the gate waits on.
- Nothing you can do on the mirror alone clears it. The probe needs a prior run
  whose **Test step itself succeeded**, not merely a green check: `decide`
  skips any candidate whose suite did not run. A docs-only pull request is
  green and does not qualify, because docs-only skips the suite. And the probe
  step sits **before** the suite steps in the same job, so a run that fails the
  probe never reaches the suite that would have qualified it.
- Editing the mirror's `ci.yml` is not the way around it. The gate rebases onto
  the mirror's default branch and reviews the difference from it, so a mirror
  whose `main` has diverged from this repository puts that divergence in every
  head's reviewed diff, as a change the head appears to revert.

So the Actions-on path validates **docs-only heads** against the real checks
today. For a head that changes code, use the Actions-off path below and say in
your comparison that the checks stage settled on a declaration. Fixing the
probe's fresh-repository case is a change to `scripts/prior-check.py`, not
something this runbook can work around; it is issue #827.

Turning them off takes a second step. An empty check list is not a pass: the
run **waits** in the checks stage, re-reading, until `checks_timeout` elapses
(global-only, default `168h`) or you end the run, as
[Keep your CI required](operators.md#keep-your-ci-required) describes. A
repository with no CI has to say so with `no_ci`, which is a trusted key, so it
can be set either on the mirror's default branch or in the home's own
`config.yaml`. The home's file is the one to use here, because it leaves the
heads you are validating exactly as they are:

```sh
gh api -X PUT repos/dayamjz/assistant-gate-mirror/actions/permissions -F enabled=false
mkdir -p "$HOME_ROOT"
grep -n '^no_ci:' "$HOME_ROOT/config.yaml" 2>/dev/null ||
  printf 'no_ci: true\n' >> "$HOME_ROOT/config.yaml"
```

The `grep` guard is there because the append is not idempotent and the
configuration is strict about it: a key appearing more than once in an object
"is refused rather than resolved to one of its values". So a second run of a
bare `>>` leaves a file that will not parse, and `service start` reads the same
resolution the service makes and refuses before launching anything.

**If the `grep` printed a line, nothing was appended: open the file and make
that line read `no_ci: true`.** The guard asks whether the key is present, which
is not the same question as whether it says what you want, and the case that
matters is a `no_ci: false` already in the file. Actions would be off and the
key would still declare that this repository has CI, so every run would wait the
full `checks_timeout` with nothing coming. Edit the line in place rather than
appending a second one, for the duplicate-key reason above.

The service reads `config.yaml` when it starts, so write this before step 6 or
restart the service after writing it; step 6 says so again. In any comparison
you report, say that the checks stage settled on a declaration rather than on
checks.

## 5. Set the gate up

```sh
./bin/assistant --home "$HOME_ROOT" init \
  --upstream https://github.com/dayamjz/assistant-gate-mirror.git
```

It prints the gate it created, the upstream row `doctor` prints and the default
branch it recorded, and it offers the `commands.test` and `commands.lint` it
found in the `Makefile`. **At a terminal it prompts `[y/N]` for each one**, and
a `y` writes that command into `assistant.json` in the working copy, where it
reaches no run until it is committed on the recorded default branch. Answer `N`
to both, or pass `--json`, which never prompts; `--accept-commands` writes them
all without asking.

Leave them unwritten and **every run holds at the test, lint and document
stages**, because this repository's default branch carries no `assistant.json`
for a run to read them from. For a shadow comparison that is usually what you
want: the gate's findings then come from the review stage, which is what an
existing review is comparable with.

If you do configure `commands.test`, do not point it at this repository's full
suite on a machine that runs other work.
[Keep `commands.test` targeted](operators.md#keep-commandstest-targeted) is the
reason: the service starts a stage command at its own scheduling priority and
lowers nothing, so a full-repository command can use every core and saturate
the host by itself.

Then check the result, and keep these facts, because the next steps and the
teardown need them:

```sh
./bin/assistant --home "$HOME_ROOT" doctor           # read the agent row
./bin/assistant --home "$HOME_ROOT" --json status    # the repository record's id
git remote -v                                        # the assistant remote's path
```

`doctor`'s last line is its verdict on whether a run can start at all. A
blocking agent row is the prerequisite above: install and authenticate one
before going on, because a run cannot reach its review stage without it.

## 6. Hand the service to the operating system's service manager

```sh
./bin/assistant --home "$HOME_ROOT" service start --install
./bin/assistant --home "$HOME_ROOT" service status
```

`--install` writes a launchd agent on macOS or a systemd user unit on Linux and
starts it there, so the service comes back after a login or a restart. The
definition's name is a digest of the home root, so a dogfood home's service
cannot collide with another home's:

- macOS: label `assistant.service.<digest>`, at
  `~/Library/LaunchAgents/assistant.service.<digest>.plist`
- Linux: unit `assistant-<digest>.service`, under the user's systemd
  configuration directory

The definition starts `assistant --home <root> service start --foreground
--managed`, so the home is in the definition and not in the environment. Only
`PATH` is carried over from the installing shell; no other variable, and no
credential, is written into it.

Record the label or unit name, the definition's path and the pid somewhere your
own process accounting can see, because a long-lived service is exactly what a
leak audit is meant to notice.

**The home's `config.yaml` is read when the service starts.** Write every key
you mean to set, `no_ci` among them, before this step, or run
`assistant service restart` after editing it, as
[Bounding what one run spends](operators.md#bounding-what-one-run-spends) says
of its own keys. A run pins the configuration it resolved at its start, so a
key added under a running service reaches neither that service nor any run it
has already begun.

Logs, all under the home: `logs/service.log` is the bounded lifecycle log and
`logs/<run>/<stage>.log` is each stage's output. `logs/managed-service.log` is
launchd's only: the definition's output file is written for launchd, and the
systemd unit leaves standard output and error to the user's journal, so on Linux
that file does not exist and the output is in
`journalctl --user -u assistant-<digest>.service`.

## 7. Send a branch through

A branch reaches the gate by being pushed to the `assistant` remote. For a
shadow run of an existing pull request, fetch its head and push that:

```sh
git fetch origin refs/pull/<number>/head:shadow/pr-<number>
git checkout shadow/pr-<number>                   # step 8 reads the run of the branch you are on
./bin/assistant --home "$HOME_ROOT" --json runs   # before, so the new run is obvious
git push assistant shadow/pr-<number>
```

The push answers at once and the run goes on in the service. The branch name is
the gate's handle on the run, so one branch per pull request head keeps the runs
apart.

The checkout is there for step 8 rather than for the push. `status` reports the
active run **of the branch the working copy is on**, so reading it from another
branch reports no active run even while the run is advancing. `runs` lists every
run whatever branch you are on, and `--cancel=RUN` reaches one by name from any
directory, so neither needs it.

### A pushed run carries no intent, and two checks go quiet without one

A run a push started has no intent recorded, and the stages after the first say
so rather than inferring one. The intent stage reports `intent-absent`, and the
review stage reports that the scope lens did not run, "the lens needs a recorded
intent to trace a change to". So nothing in a pushed run says whether what the
change touches was asked for. That is the gate being honest, not a defect, but
it means a shadow run started by `git push` is a weaker run than one a person
starts.

Starting the run from the branch is what records one. **Use this instead of the
push above, not after it.** The record holds one live run per branch, and the
two commands get there differently: a push **supersedes** the branch's
unfinished run, terminating it before it creates the new one, while this
command **attaches** to a run the branch already has and starts one only when
there is none. So pushing first and then running this attaches you to the run
the push started, which is exactly the run with no intent recorded.

```sh
git checkout -B shadow/pr-<number> <the head>
./bin/assistant --home "$HOME_ROOT" --intent 'what this change sets out to do'
```

The intent stage then reports it as authoritative acceptance criteria rather
than a hint. That command attaches rather than returning, and says so: it blocks
while the run advances. Interrupting it leaves the run going in the service,
which is where the run lives either way, so reading it afterwards is step 8 and
ending it is step 9.

The review stage's decision-model checks are a separate thing and stay off here.
They send nothing without `ASSISTANT_DECISION_MODEL_KEY` in the environment, and
that is a consent rule rather than a setting: with no key no request leaves the
machine in any mode. The review reports that they did not run. Note that step
6's service definition carries `PATH` and nothing else, so a key exported in
your own shell does not reach a service a service manager starts.

## 8. Read where it stands, and its findings

```sh
./bin/assistant --home "$HOME_ROOT" status        # with that branch checked out
./bin/assistant --home "$HOME_ROOT" --json status # stage times, every finding
```

`status` prints the stage list with the time each one took, the position the run
is at, and, when the run is held, the findings holding it in full. `--json` adds
the findings behind the holding one, each with the name to select it by.

`assistant watch` prints the fleet catch-up and then follows the event stream
until it is interrupted, so in a script use `assistant watch --once`, which
prints that read and exits.

## 9. Answer a hold, or end the run

From the branch's own registered checkout:

```sh
./bin/assistant --home "$HOME_ROOT" --answer approved      # or skipped, or cancelled
```

`fix` is the fourth answer and is not a bare word: it takes the findings for the
round to resolve, as
`--answer fix --select ID[,ID...]` with the names `--json status` shows. See
[Answering a hold](operators.md#answering-a-hold) for the whole set.

Answering the **review** stage `skipped` stops the run later rather than
sooner: the push stage needs a durable record that a completed review approved
a commit the head descends from, so it holds with `push-no-approval` and the
head is never pushed. Answer the review `approved` when you mean the run to
reach the push, pull request and checks stages; `skipped` is for the stages that
had no command to run.

**This is the part of the setup that bites.** The answer is resolved from the
directory you type it in: it needs the working copy `init` registered, standing
on the run's branch. From any other worktree of the branch, from a task's own
copy, or from the registered checkout while it stands on another branch, it
reports `this branch has no active run to answer` even though the run exists and
is held. Issue #808 has the detail. Two things do work from anywhere:

```sh
./bin/assistant --home "$HOME_ROOT" --cancel=<run>                  # end the run
./bin/assistant --home "$HOME_ROOT" --task <task> --answer <answer> --run
```

The second is only for a run a task owns. Neither is offered by the hold's own
guidance, which names `--answer` alone.

## 10. Stop it, and take it out

**Eject before you stop the service, not after.** `eject` is refused while any
run copy still stands under the home, and the refusal says who clears one: the
service decides each copy when its run finishes or at its next start. With the
service stopped, nothing is left to decide them, and the only way on is to
start it again.

```sh
./bin/assistant --home "$HOME_ROOT" --json runs             # find runs not yet finished
./bin/assistant --home "$HOME_ROOT" --cancel=<run>          # end each one
./bin/assistant --home "$HOME_ROOT" status                  # kept work, which eject refuses over too
./bin/assistant --home "$HOME_ROOT" eject --confirm         # service still up
./bin/assistant --home "$HOME_ROOT" service stop --uninstall
```

`eject --confirm` removes the gate repository, the `assistant` remote in the
working copy and the runs recorded against the gate. It is refused while a run
copy is still standing, naming each path, and while any kept work is open;
answer the kept work as [Kept work](operators.md#kept-work) describes, and run
`eject` again once the copies are gone.

`service stop` is refused while a run is under way and names `--force` as the
way through; `--force` interrupts every run at once, so ending them one at a
time with `--cancel` first leaves a record of what you did. `--uninstall` is
what stops the service manager bringing it back.

### Until #807 is fixed: a task record blocks the eject

`eject --confirm` is refused for good once any task has ever been created
against the repository, whatever state that task has been resolved to, and no
command in this build ends a task record:

```
store: a task still names a run of this repository: task <task> works on a branch
of repository <repository>
```

So if you created tasks in the dogfood home, step 10 stops at its `eject` line
with the service still up. Because the home is the dogfood's own and holds
nothing else, carry on from there: hand the service back, then remove the home
and the remote by hand.

```sh
./bin/assistant --home "$HOME_ROOT" service stop --uninstall
git remote remove assistant
rm -rf "${HOME_ROOT:?}"
```

Run `service stop --uninstall` while the definition is still installed. It
refuses with a not-installed reason once the definition is gone, so it is not a
second way to stop a service you already uninstalled. Step 10 cancelled every
run, so the stop is not refused over an active one and `--force` is not needed.

The `${HOME_ROOT:?}` form is deliberate: an unset or empty variable there would
otherwise make that command remove the wrong thing. Do this only for a home set
up for the dogfood. A home with real work in it is ejected per repository, not
removed, and a task record there is a reason to wait for #807 rather than to
delete the home.
