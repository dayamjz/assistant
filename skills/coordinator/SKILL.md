---
name: coordinator
description: Run a fleet of assistant tasks as their coordinator - create delivery and investigation tasks, steer, peek at, stop, relaunch and promote them, record an investigation's findings relayed, listen to, wait on, drain and acknowledge the wake queue, and verify supervision
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
  needs, and `assistant host install --write` adds it. For Codex use
  `--host codex` (writes `~/.codex/hooks.json` or `$CODEX_HOME/hooks.json`);
  the default `--host claude` writes Claude Code's settings. A Codex hook
  written this way does not run until you review that exact hook in `/hooks`
  and trust it. Ask the user before writing it.

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
`assistant --json tasks ID`. A task queued behind an investigation is
released, and its worker begun, as soon as that investigation's report is
filed and it reads `reported`; one whose report raised decisions reads
`held` until you answer them, and what was queued behind it is released at
the service's next release, a run finishing or a restart, rather than at
your answer.

## Read the fleet

```sh
assistant tasks                 # every task and its resolved state
assistant --json tasks ID       # one task in full, with what decided its state
assistant watch --once          # what waits on you, what landed, what is under way
```

The state is resolved from the records each time it is read. Do not infer it
from the last event you saw.

When an investigation has filed its report, `assistant --json tasks ID`
carries it at `report`: `summary`, `findings`, `evidence` (paths the worker
named), `evidence_file` (the on-disk copy under the task's directory), and
`recorded_at`. Read that field; do not ask the worker to repeat the report,
and do not relay its wording to the person (P13). The plain `assistant tasks`
line only says the report is in and points you at `--json`. A reported
investigation whose decisions are still open also lists those holds; answer
them with `assistant --task ID --answer ...` before you promote or record
findings relayed.

## Steer a worker

```sh
assistant coordinator message ID --text "what to change"
assistant coordinator message ID --text-file note.md
```

The message is recorded first and then written to the worker of the task's
latest attempt. The answer's `delivery` is `written` when it reached the
worker's input. Text is limited to 16 KiB and refused, not cut, above it. A
task with no launched worker, or one this service does not hold, is refused.
The worker reads it under a heading `Message N`, where N is the answer's
`message.id`, and acknowledges it by that number through its own
`assistant worker` route; the record then reads acknowledged.

## Peek at a worker

```sh
assistant --json coordinator peek ID
```

It reads the recent output of the worker of the task's latest attempt, as
the service holds it, as a transcript: `events` is what the worker said and
did, oldest first, each with a `kind` (`text`, `tool_call` naming the `tool`,
`tool_result`, `message`, `turn_ended`, `error`, `output`, `partial`, or
`unrecognized` naming a line type the program has no row for), and `output`
is the same events as lines you can read. Counters, status lines and thinking
are left out and counted in `omitted`. Every event is redacted of the
credential shapes the program recognizes and abbreviated, and only the most
recent events that fit in 16 KiB of `output` are shown: `earlier` counts the
events read and not shown, `dropped` the bytes the capture let go before the
read, and `liveness` is how the worker's terminal probed. When the service
hosts workers in tmux, `attach` is the command a person runs to watch the
worker and type into it; it is empty for a worker with no terminal. What
someone types there reaches the worker as its own input, the same input a
message is written to, and the program records nothing of it, so reconcile
around it as you would around any change you did not make: read the task
and its history rather than assuming your last message was the last word.

The output is for you to read and never to repeat to the person. A worker
this service did not launch, such as one an earlier service started, is
refused rather than read.

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

The promotion then sends the task's running worker its brief, the intent and
what is now expected of it, as a message recorded and acknowledged like any
other; the same brief is appended to its instructions, which a worker begun
on the task later reads. The answer's `brief.message` is that record. When no
running worker could be sent it, the promotion still stands, `brief.message`
is absent and `brief.unsent` says why; send the worker the new direction
yourself if one is running again.

## Record an investigation's findings relayed

```sh
assistant coordinator relayed ID
```

Once you have passed a reported investigation's findings on to the person,
record it. The task then reads `complete` (`state.source` is `record`,
`state.basis.relayed` is true): the service ends the worker it holds for it
and removes its copy once that is proven safe, and the stored report and
its decisions stay. It is refused for a task that is not an investigation
reading `reported` from its report: a delivery task, an investigation with
no report or with a decision still open, and one already recorded. A
relayed investigation cannot be promoted, so promote first if you mean to.

## Drain the wake queue

The service queues a wake when something needs the coordinator. Every turn
that handles a wake drains the queue before doing anything else:

```sh
assistant --json coordinator wakes          # hands you the queued wakes, oldest first
assistant coordinator acknowledge 12 13     # after you acted on wakes 12 and 13
```

- A drain hands wakes out and records none as handled. Always drain with
  `--json`: without it, `wakes` is a person's listing that hands out
  nothing.
- Act keyed by `(task_id, kind, session-ref)`, never by `handouts`. Delivery
  is at least once, so the same wake id can reach you twice after a lost
  ack; `handouts` only counts deliveries. Read the task before every act.
- Act on each wake, then acknowledge it by its `id` — every id you acted on,
  including one you decided needs nothing further after reading the task.
  Acknowledging is the only thing that records a wake as handled, and a wake
  keeps its task counted as in flight until you do.
- A wake you were handed and did not acknowledge is handed out again: after
  a service restart, and on your next drain once you are no longer
  attached. You are attached while you hold `listen`; if you hold no
  `listen`, every drain hands you the wakes you have not acknowledged yet.
- `--limit N` bounds one drain; the default is 100. Wakes past the limit
  stay queued for the next drain.
- Each wake names a `task_id` and a `kind`. For `terminal-gone`, the session
  reference is in `marker` between the case and the unobserved-since
  instant. If the task's latest attempt already names a different session,
  or the program already relaunched that one, do not call `task.relaunch`
  again. A marker carrying `:again:` is a resurface reminder for the same
  silence, not a second relaunch. Case `unobservable` means probe or wait;
  never `task.relaunch` on it — only `dead` is a crashed worker the program
  relaunches. For `hold-opened`, answer with
  `assistant --task ID --answer ...`. For `message-unacknowledged`, the
  worker has not acknowledged a message. The program's own records of
  creating and dispatching a task wake nobody.
- `acknowledge` also closes a wake `listen` printed that you dealt with
  without draining. Naming an id no wake has, or one already acknowledged,
  refuses the whole batch with code `invalid-request`.

## Be woken by the service

The service pushes each wake to a coordinator holding its wake stream:

```sh
assistant --json coordinator listen     # one line per wake, until interrupted
```

- Run it in the background if your tool can start a turn on a background
  command's output, and keep one running while work is in flight.
- It hands out and records nothing. Each line it prints is a notice that a
  wake is queued; drain to be handed it, and acknowledge it once you acted.
- Holding it is what makes you attached: wakes a drain hands you while it
  runs are not handed out again under you. When it ends, whatever you were
  handed and did not acknowledge comes back on the next drain.
- Its first line is a gap marker, and a gap later on means pushes were
  missed. On either, drain: the drain gives back every wake still queued
  that you were not already handed.
- If the service stops, `listen` exits `1`. Run `assistant coordinator
  supervision`, then start `listen` again and drain.

## Wait for a wake

If you cannot hold `listen`, block on the queue instead of polling. The
specification has the coordinator hold exactly one supervision wait whenever
work is in flight, resumed as the last action of every turn: end each turn
by arming one `await` (or by keeping `listen` running). The build does not
enforce one: a second wait armed beside the first is served too, and keeping
to one is yours.

```sh
assistant --json coordinator await --timeout 5m   # blocks, then drains
```

- It answers as soon as the queue holds a wake on offer to you, draining it
  as `wakes --json` does, or once the bound passes with an empty list. An
  empty list always means the bound passed. It drains with or without
  `--json`.
- A wake it returned is still waiting until you acknowledge it. So the loop
  is: wait, act on each wake it returned, acknowledge them, wait again. A
  wait armed before you acknowledge answers at once with the same wakes
  when you hold no `listen`.
- `--timeout` is whole seconds from `1s` to `1h`; the default is `5m`.
  Anything else is refused as incorrect usage, exit `2`, and nothing is
  clamped. Choose a bound shorter than the limit your own tool puts on one
  command, or run it in the background if your tool allows that.
- `--limit N` bounds the answer, as on `wakes`.
- If the service stops or restarts while you wait, the command exits `1`.
  Run `assistant coordinator supervision` to confirm the watch is live
  again, then arm the wait again; wakes still queued are drained at once,
  the ones you were handed before the restart and did not acknowledge
  included.
  A restart can change the queue first: on start the service relaunches a
  task whose worker's terminal is gone and whose work has not landed, up
  to `relaunch_attempts` times (2 unless configured), and records that
  task's queued `terminal-gone` wake about the replaced worker as handled
  by the program, so the wait no longer returns it. After a restart read
  `assistant tasks` as well as the queue.

## Known gaps in this build

- **Waking depends on the watch.** A push and a wait both follow the
  service's supervision recording a wake, so how soon either reaches you
  after a worker stops is the supervision's cadence. Check
  `assistant coordinator supervision` at session start and after any exit
  `1` from `await` or `listen`.
- **Being attached is your stream, not you.** The service sees `listen`,
  not your session. A `listen` left running after your session died keeps
  what you were handed until that process ends, and two streams held at
  once count as one, so a coordinator going away is not noticed until every
  stream has closed. This build does not expire or heartbeat that
  attachment, so a zombie `listen` can pin handouts until the process ends.
  If you hold `listen` and never acknowledge what you acted on, you are not
  handed it again, and it keeps the work counted as in flight: acknowledge
  as you go, and do not leave a detached `listen` running.
- **The service pushes only to `listen`.** It does not start a turn in your
  host by itself; a coordinator holding neither `listen` nor `await` is
  woken by nothing.
- **Promote an investigation before its worker ends.** Once a worker
  files its report through `assistant worker`, the task reads `reported`
  (`state.source` is `report`) whether its worker is still running or has
  ended. The program does not relaunch it, and the wake queue does not
  report its ended terminal as gone. Decisions the report raised are holds,
  and the task reads `held` until you answer them with
  `assistant --task ID --answer ...`. Once the worker has ended and nothing
  but the report's decisions holds the task, its copy is cleaned up and it
  can no longer be promoted. A stop's hold keeps the copy until you answer
  it. A reported investigation reads `reported`, never `complete`, even
  after its copy is gone, until you record its findings relayed with
  `assistant coordinator relayed ID`, which also ends its worker.
- **An orphaned command is placed as yours.** The service decides who is a
  worker by walking the caller's parent chain to the first process in a
  worker group, or that is itself a worker, so a worker's command in a
  process group of its own is still refused. A chain the service cannot
  read is refused too. What it cannot place is a command whose parent
  exited, so the kernel moved it under another process: that call is placed
  as the coordinator's and served. A worker's grant allows git and its own
  `assistant worker` route without asking, and denies nothing else.
- **No interrupt.** The service refuses `task.interrupt` on every call,
  because no adapter declares what it needs, so the client offers none.
  Stop the worker instead.
