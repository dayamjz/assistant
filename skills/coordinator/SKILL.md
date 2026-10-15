---
name: coordinator
description: Run a fleet of assistant tasks as their coordinator - create delivery and investigation tasks, steer, stop, relaunch and promote them, drain and acknowledge the wake queue, and verify supervision - through the assistant coordinator client. Use when the user asks you to coordinate, dispatch, spawn, steer or supervise assistant tasks or workers, or to act on what the fleet's wake queue reports.
---

# coordinator

`assistant` can run a fleet: tasks, each with a worker agent in a local copy
of its own, supervised by the background service. You are the coordinator.
You decide what work exists and what each worker is told; the program
records it, launches workers, watches them, and queues a wake when something
needs you.

Drive the fleet through `assistant coordinator <subcommand>`. Add `--json` to
get one document on standard output; progress goes to standard error. Exit
codes: `0` success, `1` an operational failure (a refusal, a service that is
not running, an arm verification that found no live loop), `2` incorrect
usage, which includes a request the service could not read as given.
`assistant coordinator --help` lists the subcommands, and
`assistant coordinator <subcommand> --help` lists one's flags.

Every coordinator method is refused for a caller the service places inside
a task's worker group, decided from the credentials the socket reports and
never from anything the caller sends. A refusal of that kind arrives as code
`worker`.

## Before you start

- The service must be up: `assistant service start`.
- The repository needs a gate: `assistant init` in its working copy.
- Tasks name the repository by its record's identifier. Read it from
  `assistant --json status` in the working copy, at `repository.id`.
- At the start of every session, verify supervision:
  `assistant coordinator supervision`. Exit `0` means the service's watch
  over the fleet is live. Exit `1` means it is not; the answer says whether
  to start or restart the service. Do not create work while it is not live.
- The stop guard keeps your turn from ending while work is under way and
  nothing is watching it. `assistant host install` prints the line your host
  needs, and `assistant host install --write` adds it. Ask the user before
  writing it.

## Create a task

```sh
assistant coordinator create --shape delivery --project NAME \
  --repository REPO_ID --branch task/NAME \
  --instructions "what the worker is to do"

assistant coordinator create --shape investigation --project NAME \
  --repository REPO_ID --instructions-file brief.md
```

- `delivery` works on a branch and needs `--branch`. `investigation` names
  no branch.
- `--instructions` or `--instructions-file`, exactly one. A relative file is
  read from the directory you ran the command in.
- `--overlaps TASK` claims this task overlaps another, so it waits behind
  it. With no `--overlaps` you claim independence; the program still queues
  a delivery behind a live task on the same branch.
- `--delivery gated` or `--delivery local` asks for a posture in place of the
  project's. It may be stricter than the project's and never lighter.
- `--mode` is recorded as given.

The answer comes once the task is recorded and before any worker exists.
`record.blocker_task` is set exactly when the task was queued behind other
work. Whether the worker launched is read afterwards, from
`assistant --json tasks ID`.

## Read the fleet

```sh
assistant tasks                 # every task and its resolved state
assistant --json tasks ID       # one task in full, with what decided its state
assistant watch --once          # what waits on you, what landed, what is under way
```

The state is resolved from the records each time it is read. Do not infer it
from the last event you saw.

## Steer a worker

```sh
assistant coordinator message ID --text "what to change"
assistant coordinator message ID --text-file note.md
```

The message is recorded first and then written to the worker of the task's
latest attempt. The answer's `delivery` is `written` when it reached the
worker's input. Text is limited to 16 KiB and refused, not cut, above it. A
task with no launched worker, or one this service does not hold, is refused.

## Stop a worker

```sh
assistant coordinator stop ID
```

It ends the worker, leaves the copy as it is, and answers only once the
worker is proven gone. It then opens a hold on the task, named in the
answer's `hold`. Answer that hold to decide what happens next:

```sh
assistant --task ID --answer retry   # begin the work again in the same copy
assistant --task ID --answer stop    # leave it where it stopped
```

A stop that could not be proven is refused, and no hold is opened. A task
whose work had already finished when its worker ended is answered with
`finished` set and no hold.

## Relaunch a task whose worker is gone

```sh
assistant coordinator relaunch ID
```

Use it when a task's worker died on its own: `assistant --json tasks ID`
reads `state.state` as `unknown` with `state.basis.process` as `dead`. It begins a new attempt in the task's
own copy through the same anchored relaunch the service uses at start, so
the two cannot both relaunch one task. It is refused for a live worker, and
for a task a stop holds: answer the stop's hold instead. The answer comes
once the attempt is recorded; read whether the worker launched from the task.

## Promote an investigation

```sh
assistant coordinator promote ID --repository REPO_ID --branch task/NAME \
  --intent "what the delivery is to do"
```

It turns the investigation into a delivery on the branch, in place, in the
same copy. `--intent-file` works as `--instructions-file` does, and
`--delivery` as it does on create. Promoting a task that is not an
investigation is refused.

## Drain the wake queue

The service queues a wake when something needs the coordinator. Drain it,
act, then acknowledge what you handled:

```sh
assistant --json coordinator wakes          # queued wakes, oldest first
assistant coordinator acknowledge 12 13     # the ids you handled
```

- Listing acknowledges nothing. A wake stays queued, and is listed again,
  until you acknowledge it. Delivery is at least once, so handle a wake you
  have already seen without harm.
- `--limit N` bounds one list; the default is 100.
- Acknowledging an id no wake has, or one already acknowledged, refuses the
  whole batch with code `invalid-request`.
- Each wake names a `task_id` and a `kind`. Read the task before acting:
  for `terminal-gone`, relaunch it or decide to let it go; for
  `hold-opened`, a decision waits on an answer; for
  `message-unacknowledged`, the worker has not acknowledged a message.

## Known gaps in this build

- **No blocking wait (#228).** `wakes` answers at once. Nothing here sleeps
  until a wake arrives, so you poll the queue or read `assistant watch`.
- **Workers cannot report back (#226).** A worker has no route to the
  program's event method. So a message the worker read and acted on is
  never recorded as read, and `message-unacknowledged` wakes arrive for it
  anyway; check the work before resending. An investigation cannot file its
  report, so it never resolves as reported.
- **A real worker's commands may not be placed as the worker's (#225).** The
  service decides who is a worker from the caller's process group, and
  #225 records a Claude Code worker whose git ran outside its worker's
  group. A call from outside every worker group is placed as the
  coordinator's and served, so the refusal is not established for such a
  worker. A worker's grant allows git without asking and denies nothing
  else. The refusal is tested against a process placed in a worker's group,
  not against a real worker.
- **No interrupt.** The service refuses `task.interrupt` on every call,
  because no adapter declares what it needs, so the client offers none.
  Stop the worker instead.
