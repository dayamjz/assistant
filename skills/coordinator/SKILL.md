---
name: coordinator
description: Run a fleet of assistant tasks as their coordinator - create delivery and investigation tasks, steer, peek at, stop, relaunch and promote them, record an investigation's findings relayed, listen to, wait on, drain and acknowledge the wake queue, verify supervision, reset a session from one catch-up read, and delegate a domain of tasks to a subordinate coordinator
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
`assistant coordinator <subcommand> --help` lists one's flags. Every
coordinator method is refused, with code `worker`, for a caller the service
places inside a task's worker group, decided from the credentials the socket
reports and never from anything the caller sends.

## References: read one when its trigger applies

This file holds what every turn needs. The program loads none of the files
below: read one yourself, before you act, whenever its trigger applies.

| Reference | Read it when |
| --- | --- |
| [references/catch-up.md](references/catch-up.md) | your session starts, your host cleared or compacted it, or it grew long and you reset it: start from `assistant --json coordinator catch-up` |
| [references/tasks.md](references/tasks.md) | you create a task with `--overlaps`, `--delivery` or `--mode`, read a task's `policy` or `record.blocker_task`, or read the `watch` digest |
| [references/workers.md](references/workers.md) | you message, interrupt, peek at, stop or relaunch a worker; a wake is `terminal-gone`, `message-unacknowledged`, `stall-closed` or a stall hold; a command is refused `unplaced`, `unplaceable` or `not-at-terminal` |
| [references/promotion.md](references/promotion.md) | an investigation reads `reported` or `held`: you read its report, promote it, or record its findings relayed |
| [references/kept-work.md](references/kept-work.md) | a `kept-work` wake or a `kept_work_wakes` row arrives, a task reads `dispatched` naming `kept_work`, or an answer carries `kept_work_unread` |
| [references/housekeeping.md](references/housekeeping.md) | you create a housekeeping pass, a `housekeeping_wakes` row arrives, or a pass reports or offers a retirement hold |
| [references/delegation.md](references/delegation.md) | you delegate or end a domain, a `domain_wakes` row or a `subordinate-*` wake arrives, or you are a subordinate |
| [references/waking.md](references/waking.md) | you set up `listen`, `await` or the tmux host wake, one of them exits `1`, or the service restarted |

## Before you start

- The service must be up (`assistant service start`), and the repository
  needs a gate (`assistant init` in its working copy). Tasks name the
  repository by its record's identifier: `assistant --json status` in the
  working copy, at `repository.id`.
- At the start of every session, verify supervision:
  `assistant coordinator supervision`. Exit `0` means the service's watch
  over the fleet is live. Exit `1` means it is not; the answer says whether
  to start or restart the service. Do not create work while it is not live.
- The stop guard keeps your turn from ending while work is under way and
  nothing watches it: `assistant host install` prints your host's line and
  `--write` adds it (ask the user first; `references/waking.md` for Codex).

## Create a task and read the fleet

```sh
assistant coordinator create --shape delivery --project NAME \
  --repository REPO_ID --branch task/NAME --instructions "what to do"
assistant coordinator create --shape investigation --project NAME \
  --repository REPO_ID --instructions-file brief.md
assistant tasks                 # every task and its resolved state
assistant --json tasks ID       # one task in full, with what decided its state
assistant watch --once          # what waits on you, what landed, what is under way
assistant db path               # the home's database file, to read the records yourself
```

- `delivery` needs `--branch`; `investigation` names no branch. Give
  `--instructions` or `--instructions-file`, exactly one; a relative file is
  read from the directory you ran the command in.
- The answer comes once the task is recorded, before any worker exists;
  read whether the worker launched from `assistant --json tasks ID`.
- The state is resolved from the records each time it is read. Do not infer
  it from the last event you saw, and read the task before every act.

## Drain, act, acknowledge

Every turn that handles a wake drains the queue before doing anything else:

```sh
assistant --json coordinator wakes            # hands you the queued wakes, oldest first
assistant coordinator acknowledge --task 12 --domain 3   # after you acted on them
```

- A drain hands wakes out and records none as handled. Always drain with
  `--json`: without it, `wakes` is a person's listing that hands out nothing.
  `--limit N` bounds one drain (default 100); the rest stay queued.
- Act keyed by `(task_id, kind, session-ref)`, never by `handouts`. Delivery
  is at least once, so the same wake id can reach you twice after a lost
  ack; `handouts` only counts deliveries.
- Acknowledge every id you acted on, including one you decided needs nothing
  further after reading the task. Acknowledging is the only thing that
  records a wake as handled, and a wake keeps its task counted as in flight
  until you do. It also closes a wake `listen` printed that you dealt with
  without draining.
- Name each wake by its shape: `--task N` for `wakes`, `--domain N` for
  `domain_wakes`, `--kept N` for `kept_work_wakes`, `--housekeeping N` for
  `housekeeping_wakes`, once per wake. The shapes are numbered apart; a bare
  number another shape carries is refused with exit `2` and closes nothing.
  Naming an id no wake has, or one already acknowledged, refuses the whole
  batch with code `invalid-request`.
- A wake you were handed and did not acknowledge is handed out again: after
  a service restart, and on your next drain once you are no longer attached.
  You are attached while you hold `listen`; if you hold no `listen`, every
  drain hands you the wakes you have not acknowledged yet.
- By `kind`: answer a `hold-opened` with `assistant --task ID --answer ...`,
  except a local approval hold (`task/ID/local-approval/...`), the person's
  (below), and a stall hold (`task/ID/stalled/...`), a blocker to relay. Every
  other kind is in the reference its trigger names; never `task.relaunch` on
  a `terminal-gone` of case `unobservable`. The program's own records of
  creating and dispatching a task wake nobody.

## Wait for the next wake

The specification has the coordinator hold exactly one supervision wait
while work is in flight, resumed as the last action of every turn: end each
turn by keeping `listen` running or arming one `await`. The build does not
enforce one (a second wait is served too); keeping to one is yours.

```sh
assistant --json coordinator listen              # one line per wake, until interrupted
assistant --json coordinator await --timeout 5m  # blocks, then drains
```

- Run `listen` in the background if your tool can start a turn on a
  background command's output. Each line is a notice that a wake is queued;
  it hands out nothing, so drain to be handed it.
- Otherwise block on `await` instead of polling. It drains as soon as a wake
  is on offer to you, or answers an empty list once the bound passes. A wake
  it returned waits until you acknowledge it: wait, act, acknowledge, wait
  again. `--timeout` is `1s` to `1h` (default `5m`), shorter than your tool's
  own limit on one command.
- If `listen` or `await` exits `1`, run `assistant coordinator supervision`,
  then arm again and drain; `references/waking.md` says why it ended.

## When to escalate to the person

- A decision the person owes: an `ask` finding's hold, a decision a report
  or a housekeeping pass raised, and kept work. Relay the decision, never
  the worker's wording or its `peek` output (P13).
- A local delivery task's approval hold ("You approve", PRD section 6). The
  service takes an answer to it only from a command in the foreground of a
  terminal; yours is refused with code `not-at-terminal`, recording nothing.
  Tell the person the task waits on their approval and leave the hold open.
- Blockers: a stall hold, and a housekeeping pass something still moves
  after a whole interval. Also supervision you cannot restore, and a project
  convention (`assistant-knowledge.md`) that is wrong or missing, which no
  message can change. Never report `unconfirmed` work as moving.
