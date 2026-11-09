# Being woken: `listen`, `await`, the host wake and the stop guard

Read this when you set up `listen`, `await` or the tmux host wake, one of
them exits `1`, or the service restarted.

## The stop guard

The stop guard keeps your turn from ending while work is under way and
nothing is watching it. `assistant host install` prints the line your host
needs, and `assistant host install --write` adds it. For Codex use
`--host codex` (writes `~/.codex/hooks.json` or `$CODEX_HOME/hooks.json`);
the default `--host claude` writes Claude Code's settings. A Codex hook
written this way does not run until you review that exact hook in `/hooks`
and trust it. Ask the user before writing it.

## Be woken by the service: `listen`

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
- It beats to the service once per `supervision_beacon_interval` (10s unless
  configured). A `listen` that sends no beat for three intervals, because
  its process was stopped or hung, is detached: what you were handed comes
  back on the next drain, and the `listen`, if it runs again, exits `1`
  saying its lease expired.
- Hold one `listen` at a time. Starting a second ends the first, which
  exits `1` saying another listen took the stream; what was handed out
  under the first comes back on your next drain.
- Its first line is a gap marker, and a gap later on means pushes were
  missed. On either, drain: the drain gives back every wake still queued
  that you were not already handed.
- If the service stops or the lease expires, `listen` exits `1`. Run
  `assistant coordinator supervision`, then start `listen` again and drain.

## Be woken in a tmux pane

If your host runs in a tmux pane, the service can type a notice into it for
every wake, so a wait you forgot to arm or a `listen` that died does not
leave you unwoken. Run, from inside your host (your shell tool, so it runs
in your pane):

```sh
assistant host install
```

- Run it yourself, through your shell tool. Typed at the pane's shell it
  would be the pane's foreground program and read itself as the pane's,
  so it offers no line and says so.
- Besides the stop guard's line, it prints a `notify.command` line naming
  `assistant host wake` and your pane. It writes neither to the
  configuration; ask the user to add that line under `notify: command:` in
  the home's `config.yaml` and run `assistant service restart`. It replaces
  any command that key already names.
- The notice is the same line `listen` prints: drain with
  `assistant --json coordinator wakes`, act, acknowledge. It hands nothing
  out by itself.
- It types only while the pane's tmux server and foreground program are the
  ones the installer read, and while the pane is not in copy mode, not
  synchronized with other panes, and taking input; nothing is typed
  otherwise, and that wake is not typed again. If tmux restarts
  or you move to another pane, run the installer again and update the line.
- `listen` and `await` stay the fast path; keep one armed as before.

## Wait for a wake: `await`

If you cannot hold `listen`, block on the queue instead of polling.

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
  A restart can change the queue first: under the default provider a worker
  in tmux is held again rather than relaunched, and a worker on pipes (or
  one whose terminal is gone) is relaunched in its copy up to
  `relaunch_attempts` times (2 unless configured). A relaunch records that
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
  not your session. A stopped or hung `listen` is detached three beat
  intervals after its last beat, and a new `listen` replaces an old one at
  once, but a `listen` left running after your session died keeps beating
  and keeps what you were handed until that process ends or another
  `listen` replaces it. If you hold `listen` and never acknowledge what you
  acted on, you are not handed it again, and it keeps the work counted as
  in flight: acknowledge as you go, and do not leave a detached `listen`
  running.
- **The service types into your host only in a tmux pane.** With the host
  wake set up (see "Be woken in a tmux pane"), each wake types a notice into
  your pane even when you hold neither `listen` nor `await`. Outside a tmux
  pane, or without it set up, a coordinator holding neither is woken by
  nothing. The notice is typed whatever the pane shows, so text left in your
  input is sent with it, and a burst of wakes types one notice each.
