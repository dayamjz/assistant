# Starting or resetting a session from one read

Read this when your session starts, when your host cleared or compacted it,
or when it has grown long enough that you mean to reset it.

Nothing about the fleet lives in your conversation: every fact is the
service's, resolved when it is read. So a long session is mostly states that
have since changed, and every turn pays to read them again. Shed it
routinely: end or clear the session and start the next one with one read:

```sh
assistant --json coordinator catch-up
```

Act from that document alone; do not try to recall what an earlier session
did.

- `supervision` is the verdict `assistant coordinator supervision` gives.
  Do not create work unless its `outcome` is `attached` or `started`.
- `wakes` are the queued wakes, handed out to an earlier session or not.
  Each has `id`, its number within its shape, `kind`, `line`, and
  `acknowledge`, the argument `assistant coordinator acknowledge` takes for
  it (`--task=12`, `--domain=3`, `--kept=5`, `--housekeeping=2`), plus what
  its shape is acted on by: `task` and `marker` for a task's (a
  `terminal-gone`'s session reference is in `marker`), `domain` for a
  domain's, `kept_work` (the entry's `id` under `kept`) for kept work no
  task names, and `repository`, `pass` and `detail` for housekeeping. Act
  on each as the core's "Drain, act, acknowledge" and the reference its
  kind names say, then pass its `acknowledge` to `acknowledge`. Reading
  them here handed out none of them.
- `holds` are the open decisions. `answer.args` is the command that closes
  one: run `assistant` with those arguments followed by the answer, from
  `answer.working_path` on `answer.branch` when they are set. `detail` is
  the hold's own explanation and often names the answers it takes. A run
  held at a decision carries `run` and `decision`: its `question`, its
  `options` and the `findings` behind it, as the stage reported them. A
  local task's approval hold is the person's: tell them it waits.
- `tasks` is each task's resolved state, `{id, project, branch, state,
  line}`; `line` says its next action. Read one in full with
  `assistant --json coordinator tasks ID` before acting on it, as always.
- `kept` is the open kept work, by `id` and `key`; answer one as
  `references/kept-work.md` says.

Each list holds at most 50 rows and `more_wakes`, `more_holds`,
`more_tasks` and `more_kept` count exactly the rest. Tasks a listed wake,
hold or kept entry names come first, so one is left out only when more than
50 are named; read any task a row names and `tasks` does not list with
`assistant --json coordinator tasks ID`. When `more_wakes` is not 0, drain
for the rest. The read hands out, acknowledges, answers and marks nothing, so
running it again is always safe. It is refused for a subordinate
coordinator, with code `subordinate`.
