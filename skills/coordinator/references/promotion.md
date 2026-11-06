# Reported investigations: reports, promotion, relayed findings

Read this when an investigation reads `reported` or `held`: you read its
report, promote it, or record its findings relayed.

## Read a report

When an investigation has filed its report, `assistant --json tasks ID`
carries it at `report`: `summary`, `findings`, `evidence` (paths the worker
named), `uncertainties` (what the worker could not settle) and `follow_ups`
(further work it recommends), each a list of `text` with an optional
`location`, `evidence_file` (the on-disk copy under the task's directory),
and `recorded_at`. Uncertainties and follow-ups are not findings: they raise
no hold and start nothing, so a decision the person owes is only ever an
`ask` finding's hold, and a follow-up becomes work only if you create it.
Read that field; do not ask the worker to repeat the report,
and do not relay its wording to the person (P13). The plain `assistant tasks`
line only says the report is in, counts its uncertainties and follow-ups
without their text, and points you at `--json`. A reported
investigation whose decisions are still open also lists those holds; answer
them with `assistant --task ID --answer ...` before you promote or record
findings relayed.

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
yourself if one is running again. When the worker has ended, a reported
investigation's after its promotion window among them, the promotion
relaunches one in the same copy on `relaunch`'s terms, begun with the brief
and, for a reported investigation, the stored report: `relaunch` in the
answer is the attempt claimed for it, and its worker is launched in the
background, so whether it began shows in `assistant tasks`, as for `relaunch`. A
relaunch that was refused (a hold still open on the task, the relaunch
bound) is named in `brief.unsent` and leaves the promotion standing.
Promoting an investigation whose copy has a kept-work entry answers that
entry carry on by itself (`references/kept-work.md`).

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

## Known gap: a reported investigation's worker is kept for the promotion window

Once a worker files its report through `assistant worker`, the task reads
`reported` (`state.source` is `report`) whether its worker is still
running or has ended. The program does not relaunch it, and the wake queue
does not report its ended terminal as gone. Decisions the report raised
are holds, and the task reads `held` until you answer them with
`assistant --task ID --answer ...`. The service keeps the worker for the
operator's `promotion_window` (1h unless configured) after the report is
stored, so promote within it to continue that worker; past it the worker
is ended and its copy is kept, and a promotion relaunches a worker there
from the stored report. Once the worker has ended, inside the window or
past it, a copy that holds work the default branch lacks is a kept-work
entry, which holds the project's new investigation tasks, which name no
branch as its copy named none, until it is answered;
promoting the investigation answers it carry on. The copy stays until
you record the findings relayed with `assistant coordinator relayed ID`,
which also ends any worker and removes the copy once that is proven safe;
a relayed one cannot be promoted. A reported investigation reads
`reported`, never `complete`, until then.
