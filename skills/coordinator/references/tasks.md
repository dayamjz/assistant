# Creating tasks and reading the fleet in depth

Read this when you create a task with `--overlaps`, `--delivery` or
`--mode`, read a task's `policy` or `record.blocker_task`, or read the
`watch` digest. The core skill has the plain create and the reads.

## Create a task

- `--overlaps TASK` claims this task overlaps another, so it waits behind
  it. With no `--overlaps` you claim independence; the program still queues
  a delivery behind a live task on the same branch.
- `--delivery gated` or `--delivery local` asks for a posture in place of the
  project's. It may be stricter than the project's and never lighter. The
  task as `assistant --json tasks ID` reads it carries `policy`, when its
  record holds a posture and its repository is one a run can start against:
  what its recorded posture requires (`validation`), publishes
  (`publishes`), and who lands it (`lands`; a gated task reads `code-host`, or
  `remote` for a local-path upstream). `local` reads `none`, `nothing`,
  `approval-hold`, so its change is never validated.
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

## The watch digest

`assistant --json watch --once` carries a `digest` of that read: counts of
what waits on the person, what landed unseen, what is under way, what is being
validated, and what stopped, plus `unconfirmed`, the tasks under way whose
worker said it is at work while no live terminal was seen. Do not report
unconfirmed work to the person as moving; read the task first. The digest
acknowledges no wake, so it is no substitute for your own drain and
acknowledgement.
