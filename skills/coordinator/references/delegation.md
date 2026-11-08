# Delegating a domain

Read this when you delegate or end a domain, a `domain_wakes` row or a
`subordinate-*` wake arrives, or you are a subordinate.

```sh
assistant coordinator delegate DOMAIN --delivery gated --instructions "what the domain is for"
assistant coordinator end-delegation DOMAIN
```

`delegate` records a domain of tasks as delegated and has the service start
a subordinate coordinator for it, in this home, with the instructions you
give and the posture every task of the domain takes (`--delivery` is
required; the subordinate records no posture of its own). The answer comes
once the records are written and before the subordinate exists. The
subordinate is placed by its process, as a worker is: it creates tasks that
are written into its domain, and it holds `create`, `tasks`, `message`,
`stop`, `peek`, `relaunch`, `relayed`, `wakes`, `await`, `acknowledge`,
`listen` and `supervision` over that domain's tasks alone, with `tasks`
listing the domain alone and `listen` its own domain's stream. It is refused
`promote`, `answer`, `create --housekeeping`, both delegation subcommands, `catch-up`, marking the catch-up read,
the run commands, `sync`, and the service's stop and restart, with code
`subordinate`; a task outside its domain is refused with `outside-domain`.
Its grant denies editing and git and allows the `assistant coordinator`
prefix, which is why `tasks` and `answer` are subcommands here as well as
the `assistant tasks` and `assistant --task --answer` a first coordinator
runs. With the Claude Code adapter a subordinate starts under the pipe
session provider and under `tmux`, through the same grant; under the
default `auto` it stays on pipes. With Cursor it
starts under the same grant; with Codex its start is refused and logged.
While a subordinate is starting, your own coordinator calls may answer
`unavailable` for the few moments the start takes; ask again.

While a delegation stands, wakes about the domain's tasks that the
subordinate can act on are handed to it and not to you, and decisions about
them, holds among them, are handed to you and not to it. A promotion or a
hold's answer it asks for arrives as a `subordinate-escalated` wake, with
its words on the task's `--json` answer under `escalations`; act on it or
not, and acknowledge the wake. `end-delegation` takes the domain back: the
subordinate's session is ended, and from the next drain on its rows are
yours. If you are a subordinate yourself, the program told you so when it
started you; keep to your domain and ask for a promotion when you want one.

A subordinate whose session ends reaches you as a `subordinate-gone` wake
about the domain, once per `escalation_window` while it stays gone; the
service relaunches it at a restart up to `relaunch_attempts` times, and past
that the delegation holds for you as a `hold-opened` wake about the domain.
`end-delegation` answers that hold `stop` as it ends the delegation.
A subordinate still running across a restart is placed from its record and
its process, so it stays the subordinate of its domain and `end-delegation`
ends it.
Wakes about a domain are listed under `domain_wakes` with numbers of their
own and acknowledged as `--domain NUMBER` (or `dNUMBER`); wakes about kept
work no task names are listed under `kept_work_wakes` and acknowledged as
`--kept NUMBER`:

```sh
assistant coordinator acknowledge --task 12 --domain 3 --kept 5
assistant --domain DOMAIN --answer retry     # begin its coordinator once more
assistant --domain DOMAIN --answer stop      # end the delegation
```
