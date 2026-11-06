# Steering, stopping and relaunching workers

Read this when you message, peek at, stop or relaunch a worker; when a wake
is `terminal-gone`, `message-unacknowledged`, `stall-closed` or a stall
hold; or when a command is refused `unplaced`, `unplaceable` or
`not-at-terminal`.

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
`assistant worker` route; the record then reads acknowledged. A
`message-unacknowledged` wake means the worker has not acknowledged a
message.

A message is how a note about one task reaches its worker. The repository's
project conventions are not yours to send: the worker is already told
`assistant-knowledge.md` as the repository's default branch holds it, in its
first turn, and a run's review is told it too unless the repository
suppresses project instructions. You cannot change that file
through the program, and a message that claims to amend it changes nothing
but what this one worker reads. If a convention is wrong or missing, tell
the person; it changes when a change to the file lands on the default
branch.

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

## Interrupt a worker's turn

```sh
assistant --json coordinator interrupt ID
```

It cuts short the turn the worker of the task's latest attempt is running
and keeps the worker, which then waits for its next message: send one saying
what to do instead. It answers only once the worker reads idle again and is
still running. `interrupted` is `true` when a turn was running and was read
ended, and `false` when the worker had nothing under way, so nothing was
done. It is served only for a worker hosted in tmux whose agent declares
`terminal_interrupt`, which Claude Code does; anything else is refused with
nothing done, and stopping is the path left. A worker whose input holds text
someone typed into its terminal is not interrupted either, so their text
stays theirs. A turn that has not yet begun its reply is not interrupted:
the agent would put that message back in its input, where nothing can tell
it from a person's text, so the interrupt waits about half a minute for the
reply to begin and is refused if it does not; ask again once the worker has
started answering, or stop it.

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

## The wakes about a worker

- `terminal-gone`: the session reference is in `marker` between the case
  and the unobserved-since instant. If the task's latest attempt already
  names a different session, or the program already relaunched that one, do
  not call `task.relaunch` again. A marker carrying `:again:` is a resurface
  reminder for the same silence, not a second relaunch. Case `unobservable`
  means probe or wait; never `task.relaunch` on it; only `dead` is a crashed
  worker the program relaunches.
- A stall hold, keyed `task/ID/stalled/...`, arrives as `hold-opened`: a
  worker still running whose screen has not changed through repeated
  inspections. Nothing was ended. Relay it to the person as a blocker. Its
  answers are `approve` (let it run and start the count again), `stop` (end
  it and keep its copy; the stop's own hold then asks whether to begin the
  work again) and `retry` (end it and begin it again once in the same copy);
  any other word is refused.
- `stall-closed`: the program closed a stall hold itself because the
  worker's screen changed. Tell the person that decision is no longer
  needed.

## Known gaps in this build

- **Your commands are served only with a terminal above them.** The
  service decides who is a worker by walking the caller's parent chain to
  the first process in a worker group, or that is itself a worker, so a
  worker's command in a process group of its own is still refused. A chain
  the service cannot read is refused too. A command whose chain has no
  worker on it is yours only if some process on it is in the foreground of
  a terminal, which your host's tool command has when your host runs in a
  person's terminal or a tmux pane. Anything else, a command whose parent
  exited so the kernel moved it under another process (`setsid`, `nohup`
  with `&`, a double fork) among them, is refused with code `unplaced`
  before anything is recorded, and the refusal names the processes read;
  run it again without detaching it. A push to the gate
  is held to the same chain and refused with code `unplaceable`. What the
  service establishes is a terminal's foreground, not you: a worker's
  command that detaches and then opens a terminal of its own (`script`, a
  second tmux server) is still placed as yours and served. A worker's grant
  allows git and its own `assistant worker` route without asking, and
  denies nothing else.
- **A local landing is approved at a terminal, and the service checks the
  terminal, not the person.** The one hold you cannot answer, a local task's
  approval, is refused to any command outside the foreground of a terminal,
  and your host runs your commands outside one. That is the whole of the
  check: it does not name you, and it does not know who is at a terminal.
- **Interrupt only in tmux, and only for Claude Code.** The service serves
  `task.interrupt` only for a worker hosted in tmux whose agent declares
  `terminal_interrupt`. A worker on the pipe provider, or of an agent that
  declares none (Codex and Cursor run on pipes), is refused; stop it
  instead.
