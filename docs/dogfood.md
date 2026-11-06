# Running the gate on this repository, in shadow

This is a runbook for validating this repository's own pull requests with the
gate while nothing the gate does reaches this repository. It is written to be
followed from nothing: a fresh session with a checkout, a Go toolchain and `gh`
signed in needs no other context.

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

## 1. Build the binary

Build from the clone you are going to validate, so the gate under test is the
build you are reading:

```sh
make build        # writes bin/assistant
./bin/assistant --version
```

Every command below is that binary. It is written into the gate's hooks and into
the service definition by absolute path, so build it where it can stay.

## 2. Give it a home of its own

The home root is `ASSISTANT_HOME`, or the `--home PATH` global flag, which is
accepted before or after the verb. Prefer the flag in a runbook: an exported
variable is lost between shells, and a command that silently acts on the default
home is the one mistake this setup exists to avoid.

```sh
HOME_ROOT=~/.assistant-dogfood
```

Nothing creates it ahead of time; `init` does.

## 3. Clone the repository

```sh
git clone https://github.com/dayamjz/assistant.git assistant-dogfood
cd assistant-dogfood
```

Leave `origin` as it is. `init` is told the upstream explicitly in step 5, so
`origin` is only where the clone fetches from and is never a run's push target.

## 4. Create the mirror and seed its default branch

```sh
gh repo create dayamjz/assistant-gate-mirror --private
git remote add mirror https://github.com/dayamjz/assistant-gate-mirror.git
git push mirror main:main
```

The mirror needs the default branch before a run reaches the pull request stage,
because that is the base the gate opens against.

**Decide about the mirror's Actions before the first run.** The workflows come
with the clone, and `ci.yml` and `repeat.yml` both trigger on every pull
request, so a pull request the gate opens on the mirror starts the full matrix
there. On a public repository hosted runner minutes are not billed; on a private
mirror they are, and the macOS job is both the longest and the most expensive.
Either accept that cost deliberately, or turn Actions off on the mirror:

```sh
gh api -X PUT repos/dayamjz/assistant-gate-mirror/actions/permissions -F enabled=false
```

With Actions off, the gate's checks stage meets a pull request that has no
checks and holds for your decision, which is the behavior of any repository
with no CI registered. It is not the same as validating against this
repository's real checks; say which one you did when you report a comparison.

## 5. Set the gate up

```sh
./bin/assistant --home "$HOME_ROOT" init \
  --upstream https://github.com/dayamjz/assistant-gate-mirror.git
```

It prints the gate it created, the upstream row `doctor` prints, the default
branch it recorded and the commands it found in the checkout. It writes no
commands: `init` offers `commands.test` and `commands.lint` from the `Makefile`
and leaves them for you, and this repository's default branch carries no
`assistant.json`, so **every run holds at the test, lint and document stages**
until you configure them or answer each one. For a shadow comparison that is
usually what you want, because the gate's findings then come from the review
stage, which is what an existing review is comparable with.

Do not configure `commands.test` as this repository's full suite on a machine
that runs other work: the suite takes every core and the stage runs it outside
whatever scheduling the rest of the machine uses.

Check the result and keep these two facts, because the next steps and the
teardown both need them:

```sh
./bin/assistant --home "$HOME_ROOT" --json status    # the repository record's id
git remote -v                                        # the assistant remote's path
```

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

Logs, all under the home: `logs/service.log` is the bounded lifecycle log,
`logs/managed-service.log` is what the service manager captures, and
`logs/<run>/<stage>.log` is each stage's output.

## 7. Send a branch through

A branch reaches the gate by being pushed to the `assistant` remote. For a
shadow run of an existing pull request, fetch its head and push that:

```sh
git fetch origin refs/pull/<number>/head:shadow/pr-<number>
./bin/assistant --home "$HOME_ROOT" --json runs   # before, so the new run is obvious
git push assistant shadow/pr-<number>
```

The push answers at once and the run goes on in the service. The branch name is
the gate's handle on the run, so one branch per pull request head keeps the runs
apart.

## 8. Read where it stands, and its findings

```sh
./bin/assistant --home "$HOME_ROOT" status        # from the branch's checkout
./bin/assistant --home "$HOME_ROOT" --json status # stage times, every finding
```

`status` prints the stage list with the time each one took, the position the run
is at, and, when the run is held, the findings holding it in full. `--json` adds
the findings behind the holding one, each with the name to select it by.

Do not use `assistant watch` in a script or a non-interactive session: it holds
the stream open and prints nothing until it is interrupted.

## 9. Answer a hold, or end the run

From the branch's own registered checkout:

```sh
./bin/assistant --home "$HOME_ROOT" --answer approved      # or skipped, cancelled, fix
```

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

In this order:

```sh
./bin/assistant --home "$HOME_ROOT" --json runs            # find runs not yet finished
./bin/assistant --home "$HOME_ROOT" --cancel=<run>          # end each one
./bin/assistant --home "$HOME_ROOT" service stop --uninstall
./bin/assistant --home "$HOME_ROOT" eject --confirm
```

`service stop` is refused while a run is under way, and names `--force` as the
way through; `--force` interrupts every run at once, so ending them one at a
time with `--cancel` first leaves a record of what you did. `--uninstall` is
what stops the service manager bringing it back.

`eject --confirm` removes the gate repository, the `assistant` remote in the
working copy and the runs recorded against the gate. It is refused while a run
copy is still standing, and while any kept work is open; answer those first, as
[Kept work](operators.md#kept-work) describes.

### Until #807 is fixed: a task record blocks the eject

`eject --confirm` is refused for good once any task has ever been created
against the repository, whatever state that task has been resolved to, and no
command in this build ends a task record:

```
store: a task still names a run of this repository: task <task> works on a branch
of repository <repository>
```

So if you created tasks in the dogfood home, `eject` cannot finish. Because the
home is the dogfood's own and holds nothing else, the way out is to stop handing
the service over and then remove the home and the remote by hand:

```sh
./bin/assistant --home "$HOME_ROOT" service stop --uninstall --force
git remote remove assistant
rm -rf "${HOME_ROOT:?}"
```

The `${HOME_ROOT:?}` form is deliberate: an unset or empty variable there would
otherwise make that command remove the wrong thing. Do this only for a home set
up for the dogfood. A home with real work in it is ejected per repository, not
removed, and a task record there is a reason to wait for #807 rather than to
delete the home.
