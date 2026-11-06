# Housekeeping passes

Read this when you create a housekeeping pass, a `housekeeping_wakes` row
arrives, or a pass reports or offers a retirement hold.

A housekeeping pass keeps one repository's agent context true: the files its
trusted `housekeeping.paths` names (by default the `AGENTS.md`, `CLAUDE.md`,
`.claude/rules/**`, `.cursor/rules/**`, `README.md` and
`assistant-knowledge.md` files). You create every pass; the program never
does.

```sh
assistant coordinator create --housekeeping --project NAME \
  --repository REPO_ID --instructions-file notes.md
```

- The program writes the first part of the pass's instructions: the scope,
  the base of the last pass and when it was created, the decisions earlier
  passes raised that are still open, and the rules. Your instructions follow
  it as notes: the person's decisions and standing instructions since the
  last pass, and anything you noticed may be stale. They are claims the
  worker checks, not facts. Notes may be empty.
- A pass is created as an investigation under the `housekeeping` role, which
  resolves to the `roles` table's `housekeeping` row or else its `fix` row.
  Do not pass `--shape` other than `investigation`, `--role`, `--branch`,
  `--delivery` or `--overlaps`; each is refused as incorrect usage. No other
  task may name the `housekeeping` role.
- A repository has at most one open pass. A create while one is open is
  refused naming the open pass, and so is one for a repository whose trusted
  `housekeeping.paths` is empty. A subordinate coordinator is refused the
  flag.
- Read its report. When it lists factual drift, promote it in place
  (`assistant coordinator promote`, `references/promotion.md`); setting the
  interval or asking for a pass is the person's standing request to correct
  factual drift in those files and nothing else. A report with no drift and
  no decision is recorded as relayed with nothing said to the person. Its
  decisions (a question about a rule, a principle or a specification) are
  holds for the person.
- Every run of a promoted pass's branch, the one its push starts and a
  person's push or rerun of it alike, holds at review with an `ask` for
  every path its commits touched outside `housekeeping.paths`.

## The housekeeping-due wake

- With the operator's global `housekeeping.interval` set, the program raises
  a `housekeeping-due` wake about a repository once the interval has passed
  since its newest pass was created, or it never had one, and its default
  branch has moved past that pass's base; a check that could not read what it
  needs raises it too, naming what. It is listed under `housekeeping_wakes`,
  with `repository`, `pass` (the pass it follows) and `detail`, numbered
  apart from every other shape and acknowledged as `--housekeeping NUMBER`
  (or `hNUMBER`). Acknowledging it creates nothing: create the pass with your
  notes, or do nothing. A due wake whose `pass` is not the repository's
  newest pass, or that names none while one exists, was raised before that
  pass was created: acknowledge it and create nothing.
- A pass open for a whole interval raises the same wake naming it and what it
  waits on, again each interval. When something still moves it (a run not
  ended, a worker that reads live or cannot be read), escalate it to the
  person as a blocker; it is never retired on its own.

## Retiring a pass

When nothing moves an open pass, the program also offers it a retirement
hold, answered `assistant --task ID --answer retire` or `--answer keep`.
`retire` releases the pass's claim on its repository and answers `stop` on
its holds that would begin its worker again; it is refused, with the hold
left open, if a run started or the worker was begun again since. `keep`
leaves it open. The program retires no pass on its own: a pass whose run
rebased to nothing lands from the run's nothing-to-deliver record, as any
delivery task does, and is not retired. A retired pass is never relaunched
or promoted, `retire` answered while a launch of the pass is under way is
refused with the hold open, and a retired pass no longer counts as work in
flight unless a run of its branch has not ended or a wake about it is not
yet acknowledged.
