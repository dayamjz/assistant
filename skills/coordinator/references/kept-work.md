# Kept work

Read this when a `kept-work` wake or a `kept_work_wakes` row arrives, a task
reads `dispatched` naming `kept_work`, or an answer carries
`kept_work_unread`.

## The kept-work wake about a task

For `kept-work`, the task's copy, or the copy of a run attributed to it, was
kept because its work is not anywhere safe; the entry is in
`assistant --json status` under `kept_work` and in the catch-up read under
`kept`. Tell the person: it is theirs to decide.

## The answers, and who gives them

An answer is given like a hold's:
`assistant --task ID --answer land --hold kept-work/N`, or `--run=RUN` in
place of `--task` for a run's copy.

- Land validates the copy's HEAD as a push would.
- `--answer carry-on` creates a new task of the same project and branch in
  the kept copy, with a note, and names it in the answer (`task`), so the old
  task stays as it ended; a run's copy cannot be carried on.
- `--answer discard` shows what would be lost with a digest, which you may
  read (each path and name also in `path_bytes` or `name_bytes`, its exact
  bytes in base64, since a JSON string loses a byte that is not UTF-8), and
  without `--confirm` removes nothing. With `--confirm DIGEST` from a
  terminal's foreground, a task's clone or a run's worktree is removed when
  that digest still seals the copy. A confirmation is the person's alone: it
  is refused under `--json` and from any process not in the foreground of a
  terminal, which your tool's commands are not, so never try to give one.
- `--answer remove-anyway` is for a copy whose discard preview refuses
  (unsupported layout, unverifiable copy, or a loss the upstream or
  default branch could not establish), read afresh each time. Alone, when
  no claim and no discard set-aside stands on the copy, it shows why
  discard refuses, and nothing of the loss, and records nothing; that
  preview is open under `--json` and off a terminal. A standing land,
  carry-on, discard, or remove-anyway claim, a claim on another open
  entry of the same copy, or a discard set-aside refuses the alone answer
  too. With `--force` from a terminal's foreground, naming the same task
  or run, it releases the entry and every other open unclaimed entry of
  that copy, and keeps the copy. The confirmation is refused when the
  discard preview reads the copy's loss, while a claim or a discard
  set-aside stands, under `--json`, and from any process not in a
  terminal's foreground; never try to give the confirmation yourself. A
  discard refused at its removal puts the copy back and is shown and
  confirmed again; remove-anyway is refused for it.

To let the new tasks on the entry's branch go ahead, tell the person their answers are
land, carry-on, discard with `--confirm`, or remove-anyway with `--force` when
a discard preview refuses, or committing or removing its uncommitted work and pushing
its commits, after which the program removes the copy on that proof when it
next looks at it, which is not immediate, and with it, unchecked, files git
ignores, commits only a reflog reaches and the copy's hooks, which the discard
preview lists, so they should copy out what they want first; removing it by
hand leaves the entry open.
Promoting an investigation whose copy has an entry answers that entry carry
on by itself.

## New tasks it holds

While the project has an open entry, a task you create in that project **on
the entry's branch** is recorded and
not started: it reads `dispatched` and its answer (`task.get`) names the
entries holding it under `kept_work`; it starts on its own once they are
answered or closed. The entry's branch is in `status` under `kept_work`, so
read it before you promise a new task will start. A task of the same project
on any other branch starts as usual: an entry holds the work that would stand
where its copy's work is, and nothing else. An investigation task names no
branch, and is held only by an entry whose own owner named none. An answer
carrying `kept_work_unread` could not read
whether kept work holds the task, so it names no entry either way: ask again
rather than telling the person it is starting. Tasks on other branches, tasks
in other projects, work
already running and a person's own push are not held.

## Kept work no task names

A kept entry no task names, such as the run of the person's own push,
reaches you as a `kept-work` wake about the entry instead, listed under
`kept_work_wakes` with the entry's id in `kept_work`, its `project` and its
`branch`, with numbers of its own, and acknowledged as `--kept NUMBER` (or
`kNUMBER`). Acknowledging it closes the wake and answers nothing: the entry
stays listed until it is answered or its work lands.
