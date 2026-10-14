---
name: assistant
description: Validate committed changes through the assistant gate - intent, rebase, review, test, document, lint, push, pull request and checks - before they reach the shared remote. Use when the user asks to run assistant, to gate, validate, or ship a branch through assistant, to push through the assistant remote, or to answer a decision an assistant run is holding on.
---

# assistant

`assistant` is a local gate. A branch handed to it is walked through nine
stages in a fixed order - intent, rebase, review, test, document, lint, push,
pull request, checks - and nothing reaches the shared remote until each stage
has cleared. Review runs in a separate agent from the one that fixes, so a
review never checks its own fix.

Drive it through the `assistant` command. Add `--json` to any command for one
structured document on standard output; progress goes to standard error.
Exit codes: `0` success or a normal decision point, `1` an operational
failure, `2` incorrect usage.

## Before you start

- The work must be **committed** on a **feature branch**. The gate validates
  committed history, never the working tree.
- The repository needs a gate: `assistant init` creates or repairs it and adds
  a remote named `assistant`. Run it once per working copy.
- The service must be up: `assistant service start`.
- `assistant doctor` says whether a run can start at all and what stops one.
  Act on what it reports before starting.

## Start a run

From the branch, start a run with the user's intent:

```sh
assistant --intent "<what the user set out to accomplish>"
```

It blocks until the run needs a decision or ends; review can take several
minutes. Do not cancel it because it seems slow. `assistant status` from a
separate call shows where the run stands without disturbing it.

Pushing the branch to the gate also starts a run, without an intent:
`git push assistant BRANCH`. Prefer `--intent`, because review uses the
intent to tell a deliberate decision from a mistake. Write it as the user's
goal in their terms, with the decisions and tradeoffs they made and anything
they asked for that might look surprising in the diff - not a summary of the
diff.

`assistant` with no flags on a branch that has a run attaches to it; with no
run it starts one.

## Answer a decision

When a stage cannot decide, the run holds and the output names the findings
holding it. `assistant --json status` lists every finding of every stage with
its identifier, under `active_run.stages[].report.findings`. Each finding has
an `action`:

- `fix` - mechanical; you may ask for the fix on your own judgment.
- `note` - informational; nothing to do.
- `ask` - challenges the user's intent or changes product behaviour. That call
  belongs to the user: relay the finding's identifier, location and full
  description exactly as written, ask how to proceed, then answer.

Answer with one of:

```sh
assistant --answer approved     # accept the stage as it stands and continue
assistant --answer skipped      # skip this stage
assistant --answer cancelled    # end the run here
assistant --answer fix --select ID[,ID...] [--note TEXT] [--finding TEXT]
```

A fix round is the pipeline's own: its fixer edits the run's isolated copy,
the stage runs again, and it holds again so you see the result before
anything is pushed. Never fix a finding by editing the code yourself while a
run is active. At most three rounds can be asked for at one stage in one
run. The push, pull request and checks holds offer no fix round.

Each `--answer` blocks until the next decision or the end. Read every return
and answer again until the run is finished.

## Outcomes

- `decision` - waiting on an answer. Answer it as above.
- `executing` - between two decisions, or waiting on the pull request's
  checks, or stopped with nothing moving it; nothing to answer. Do not
  assume the service carries it on: read the answer's next action and
  whether the run is advancing. If it is not advancing, the run has stalled,
  so act on the next action it names rather than wait.
- `checks-passed` - the run walked the gate to its end and the pull request
  is open. Stop driving: tell the user the pull request is ready for them to
  review and merge. The gate does not merge unless the operator's
  `autonomy.merge` setting lists this repository, and even then you stop
  driving here.
- `passed` - the pull request merged.
- `failed` - the run ended without a verdict: a bound stopped it, a stage
  could not run, or the pull request was closed. Read the answer's next
  action first. When a bound stopped it, the answer names the bound: a round
  limit points at that stage's `fix_rounds` key and a step budget at
  `run_budget`, so tell the user which setting to raise, then start a fresh
  run. A convergence park means the last fix round changed nothing, so
  raising a limit will not help; take the stage's findings to the user.
  Nothing resumes the stopped run itself. Otherwise fix what the reason
  points at in a new commit on the same branch, and start a run again.
- `cancelled` - a person ended it at a hold.

## Repository configuration

A repository with no CI must declare `no_ci` in its trusted configuration,
or an empty check list is never treated as a pass. Commands that execute -
`commands.test`, `commands.lint` and the rest - and `no_ci` are read from
`assistant.json` on the **default branch** or from the home's `config.yaml`,
never from the branch under validation. Status lists a value the pushed
branch tried to set as rejected from the run's configuration; that is the
rule working, not an error. A change to `assistant.json` takes effect only
once it has landed on the default branch. With no test or lint command the
matching stage holds for a decision rather than reporting a pass it did not
establish.

## Inspecting state

```sh
assistant status            # repository, gate, service, run, branch
assistant --json status     # the same as one document, with every finding
assistant runs              # recent runs, newest first
assistant watch --once      # everything in flight and everything waiting on you
assistant --cancel          # end this branch's run, wherever it stands
```
