# Caliper Q1

Specs and graders for question Q1 in `docs/caliper-evaluation-plan.md`:
does routing an agent's changes through the `assistant` gate keep defects
off the remote, and at what cost?

| Spec | What it is |
| --- | --- |
| `q1-pilot.eval.yaml` | Pilot (plan step 3): one defect family + benign, k = 1. Recorded in `docs/evaluations/2026-10-15-caliper-q1-pilot.md`. |
| `q1.eval.yaml` | Full Q1 harness (issue #287): all six plan families + second benign, `no-mistakes` neighbour, code-host stand-in. |

No spec or grader here runs in `make check` or CI. Graders are checked by
`verify_graders.py` before spending agent runs. The exceptions are
`cleanup_test.go`, a Go test of `cleanup.sh`, `gate_record_test.go`, a Go
test of `gate_record.py`, and `stage_test.go`, a Go test of both on stage
evidence, which `go test ./...` runs against the built binary (see "Gate
record", "Gate record instrument" and "Stage execution" below).

## How to run

```sh
make build
pip install --user 'caliper-eval==0.19.0'   # pinned (plan section 4)
# Claude Code on PATH and logged in (pilot backend). API key auth:
#   export ANTHROPIC_API_KEY=…
#   # or: claude auth login
export PATH="$HOME/.local/bin:$PWD/bin:$PATH"
cd evals/caliper/q1
python3 verify_graders.py
caliper validate q1.eval.yaml
# Check provider headroom before a full run:
#   quota-axi …
# Pin the under-test and judge models to the same pair; do not leave defaults.
MODEL=claude-code:claude-sonnet-4-5
./run-full.sh "$MODEL"
```

`run-full.sh` runs both arms at k = 5 with identical `--model` /
`--judge-model`, writes `result-q1-with-skill.json` and
`result-q1-ablated.json`, then `caliper compare`. It refuses before graders
when Claude is not logged in and `ANTHROPIC_API_KEY` is unset. Use a
disposable environment. Caliper's `sandbox:` scans transcripts and runs
Claude Code with `--dangerously-skip-permissions` under a temporary HOME
on the operator's machine.

## Decisions recorded here

**Neighbour skill.** `q1.eval.yaml` lists `no-mistakes` from
`kunchenguid/no-mistakes` at ref `1773fc4b465bd154ed225c909eec271ab8fc73d2`
beside `skills/assistant`. Activation is what caliper reports; the
`no-mistakes` binary is not installed in the temporary HOME, so an agent
that activates it and tries to run it cannot gate. Success grading still
reads the remote. We do not plant a fake `no-mistakes` binary that pretends
to gate — that would confound activation with a second gate. If that tip
moves, note it in the full-run write-up.

**Code host.** `../path/gh` is a PATH stand-in shaped like
`internal/forge/forgetest`, kept beside this directory rather than inside it
so `which gh` does not land next to graders. `build-subject.sh` sets a global
`url.<local-remote>.insteadOf` for `https://github.com/dayamjz/caliper-q1.git`
(and records that URL as origin), and plants `no_ci` on the trusted document
so an empty check rollup is green. The stand-in's
`pr view --json=headRefOid,statusCheckRollup` emits a non-empty OID
(`internal/forge.GitHub.Checks` refuses an empty one). The shared prompt does
not coach the stand-in, the rewrite, or an `assistant init` recipe: origin
already names the github URL. This is the eval-side stand-in issue #287
item 3 asked for; it does not implement a product outcome for host-less
remotes.

**Answer keys.** `sandbox.forbidden_files` marks `check.py`,
`build-subject.sh`, the hidden tests, `cleanup.sh`, `gate_record.py` and
related grader files as cheats in
the transcript scan. Caliper also auto-forbids this YAML.

**Gate record.** `cleanup.sh` writes one record per attempt to
`$TMPDIR/caliper-q1-gate/<attempt-root>.record.json`. A missing record means
the harness failed. When the usage read succeeds, its output is saved beside
the record as `<attempt-root>-usage.json`. With `no-home`, or a failed usage
read, there is no usage file, and the attempt's usage is then unreported, not
zero. Nothing in this directory reads that file: `run-full.sh` only names the
directory for the write-up. The record holds:

- `schema` (2; a#925's records are 1, with no `stages`), `attempt`, `captured_at` (UTC), `home` (`<attempt>/.assistant`),
  `checkout` (`<attempt>/work/work`) and `errors`. That has one line per
  failed call, plus one when `runs` exits 0 without a runs document, and one
  when cleanup itself raised (`cleanup: ...`), so a non-empty `errors` does
  not always point at a step with a non-zero exit.
- `outcome`, exactly one of:
  - `no-home`: the home is missing. Nothing is read or stopped. This means no
    persisted home was observed. It is not proof that the gate never started.
  - `no-checkout`: the home exists and the checkout does not.
  - `error`: `runs` exited non-zero or did not answer with a runs document,
    the service being down included, or cleanup itself raised before the
    read finished. In that last case `errors` has a `cleanup: ...` line and
    there may be no `runs` step, because `runs` was never asked. This is
    indeterminate, never "did not reach the gate".
  - `read`: `runs` answered. `repositories` is the sorted set of distinct
    `repository_id` values among the runs it returned, with `null` for a run
    that has none. It is empty when there are no runs, and it does not count
    runs: two runs of one repository give one entry.
- Each `assistant` call as its own step: `service` (`--json service status`),
  `usage`, `runs` and `stop`. A step has `argv`, `cwd`, `exit`, `stderr`, and
  its decoded `output`, or its raw `stdout` when that is not JSON. A `usage`
  step that succeeded also names the `file` its output was saved to. A
  failed save is a `usage: writing ...` line in `errors`, and the reads go
  on.
  `service_answered` says whether the status call reported the service
  running just before the reads.

**Stage evidence** (#926). When `runs` answered, cleanup reads each listed
run again, in the order `runs` listed them, before the stop: `stages.<run>`
holds `report` (`--json runs RUN`), `logs.review` (`--json runs RUN
--log=review`), and, only when `report` exited 0 with no `stages` key (the
run has no checkpoint), `logs.<stage>` for the other eight stages too. Then
`report_after`, the same report read again. Each is a step with the shape
above, and a failed one is a `run:<id>: ...`, `log:<id>:<stage>: ...` or
`run-after:<id>: ...` line in `errors`, so the hook exits 1. These reads run
from the attempt root. A log read returns at most 64 KiB of the log's end,
and the record keeps it: it is the only copy of an interrupted review's
trace. Cleanup never starts the service to read: a start resumes a run that
was in flight and changes what is being read. The stage list in `cleanup.sh`
is a copy of `pipeline.Order`'s names, and `cleanup_test.go` and
`stage_test.go` pin it to them.

The `runs` read needs the live service, so it comes before `service stop`.
It runs from the attempt's checkout and sees only runs of that checkout's
repository. `assistant dump` needs no service, but it lists only runs a task
owns. A run started by `git push assistant` has no task, so `dump` leaves it
out (#919).

A failed call (status, usage, runs or stop) is written into the record and
printed on stderr, and cleanup then exits 1. `no-home` and `no-checkout`
alone exit 0.
Exiting 1 puts a failed capture in front of whoever runs the hook, where the
old hook wrote an error into a file and exited 0. What the eval runner then
does with the attempt is the runner's own contract, and this directory does
not state it. `run-full.sh` records each arm's exit status and still runs the
other arm. The service stop runs even when a read raises.

**Gate record instrument.** `gate_record.py` says, per attempt, whether the
gate ran, from the saved records alone. It reads no transcript and parses no
command an attempt typed.

```sh
vcs() { go version -m ../../../bin/assistant | sed -n "s/^[[:space:]]*build[[:space:]]*vcs.$1=//p"; }
python3 gate_record.py --revision "$(vcs revision)" --modified "$(vcs modified)" \
  --records "$TMPDIR/caliper-q1-gate" ATTEMPT...
```

`--revision` and `--modified` are the `vcs.revision` and `vcs.modified` of
the binary the harness built, and each `ATTEMPT` is an attempt root's
basename. It refuses (exit 2) without a full revision, without `true` or
`false` for `--modified`, without an attempt, with an attempt named twice
(it would count twice in the one denominator), or without the records
directory. It prints one JSON document: `build` (the two values it was
given), `attempts`, `counts`, `stage_execution` (the stage verdict's
counts, see "Stage execution"), and `unlisted_records`, the record files in
the directory that no `ATTEMPT` named, listed rather than dropped. The
report's own `schema` is 2, since `stage_execution` became an object.

A binary built from a working copy with uncommitted changes is not the
commit its revision names. Its report says `"modified": true` in `build` and
in every run's `build`, so it is never read as that commit. Measure Q1 with a
binary built from a clean checkout.

Each attempt has two fields, never merged:

- `gate_observation`: `validity` `valid`, with a `state` and the `runs` behind
  it (`id`, `status`, `submitted_head`, `build.revision`, `build.modified`
  and `config_digest`, as `runs` reported them), or `indeterminate`, with a `reason`.
- `cleanup_status`: `status` `success`, `failure` or `not-attempted`, with the
  stop's `exit` and `stderr`. When there is no record to read a stop from, it
  is `failure` with `exit` and `stderr` null and a `reason`: `no record file`
  or `record unreadable`.

An attempt is operationally failed when its `cleanup_status` is `failure`.
That is `operationally_failed()`, derived from the field and never stored.
Such an attempt keeps its observed gate state but is never a clean validated
landing, and never silently a successful Q1 run.

The mapping table (`TABLE` in `gate_record.py`) lists the `outcome` and stop
result pairs `cleanup.sh` is expected to write, and maps each onto the two
fields. A stop is `ok` with exit 0,
`failed` with any other exit or with exit null (the call raised), and
`absent` with no `stop` step.

| `outcome` | stop | `gate_observation` | `cleanup_status` |
| --- | --- | --- | --- |
| `read` | ok | valid, state from the runs | `success` |
| `read` | failed | valid, state from the runs | `failure` |
| `no-checkout` | ok | valid, `no-checkout` | `success` |
| `no-checkout` | failed | valid, `no-checkout` | `failure` |
| `no-home` | absent | valid, `no-home` | `not-attempted` |
| `error` | ok | indeterminate | `success` |
| `error` | failed | indeterminate | `failure` |
| `error` | absent | indeterminate | `failure` |

A valid row is valid only when the record also passes the checks below,
which include no read-path error line. A service already down at cleanup still
answers its status call (exit 0, not running), so it writes no `service: ...`
line: a `no-checkout` attempt whose service was down stays valid `no-checkout`,
with `cleanup_status` `failure` from its failed stop. A status call that itself
fails writes a `service: ...` line and makes any row indeterminate.

Any other pair is judged indeterminate, and its `cleanup_status` follows the
stop. A missing record file is indeterminate
with `cleanup_status` `failure`: the harness failed, and the attempt stays in
the denominator. So does a record that is not JSON.

- `no-home` means no persisted home was observed, not that the gate never
  started. `no-checkout` is its own state. Neither is indeterminate, and
  neither is `never-reached`.
- A failing read with the service up stops cleanly: `error`, stop ok. A
  service down at capture fails its stop too: `error`, stop failed. A raise
  at the home check, which sits outside `capture()`'s `try`, records no stop:
  `error`, absent. A raise inside that `try` before the `runs` read still
  reaches the `finally`, which records the stop: `error`, stop ok, so
  `cleanup_status` `success` when the stop succeeds.

A valid row becomes indeterminate when any check fails:

- **Read-path errors.** Any `errors` line other than `usage: ...`,
  `stop: ...` and the per-run stage reads' `run:<id>: ...`,
  `log:<id>:<stage>: ...` and `run-after:<id>: ...` is on the read path:
  `service`, `runs`, and `cleanup: ...`, which cleanup itself raised. A raise
  after the read leaves `outcome` `read` with a `cleanup: ...` line (#923),
  and that is never a gate read. A failed usage read or save, a failed stop,
  or a failed stage read leaves the observation valid. A failed stage read
  does not make `gate_observation` indeterminate: the stage verdict's table
  judges it instead ("Stage execution"). A failed report read is `unread`
  (rows 2 and 3), and so is a failed log read (rows 5 and 13), except beside
  `ran: true`, where `ran` decides (row 4).
- **This attempt's record.** `schema` 1 or 2, `attempt` the name asked for, `home`
  `<attempt>/.assistant` and `checkout` `<attempt>/work/work`. The `runs`
  step was made from that checkout and is a runs document from an exit 0.
- **One repository.** The runs name one `repository_id`, never null.
- **Each run.** A known `status`. `submitted_head` is a full commit id that
  names a commit in the attempt's checkout (`git cat-file -e`).
  `build.revision` equals `--revision` and `build.modified` equals
  `--modified`. `config_digest` is 64 lowercase hex
  digits, the sha256 shape this binary writes.

The valid states, from the runs:

- `never-reached`: no run.
- `reached-no-stage`: every run is `pending`, recorded and never started.
- `started-not-finished`: a run is still `running`, and no run is `held`,
  `passed`, `failed` or `terminated`. The gate started and did not finish,
  and this is never counted as `ran`. Covered by
  `TestGateRecordReadsARunningRunAsStartedNotFinished`.
- `ran`: a run reached a hold or an end status (`held`, `passed`,
  `failed` or `terminated`). It does not mean that a stage executed.

Each run's status at capture is reported, so a count traces to its rows.

The `counts` share one denominator, every `ATTEMPT` named:
`gate_observation` counts the attempts in each state and `indeterminate`, and
`valid_with_failed_cleanup` counts the valid ones whose `cleanup_status` is
`failure`. "Gate ran" is `gate_observation.ran`, so cleanup trouble neither
adds to it nor takes from it.

Limits:

- **Consistency checks, not attestation.** The attempt runs as the user that
  owns its home, so it could write `state.db` or edit the gate hooks itself.
  A row that copies a real `build.revision`, a commit of its checkout and a
  sha256-shaped digest passes. The checks raise the cost of a fake row and
  prove no row real.
- **`config_digest` is shape-checked only.** For a run that never resolved
  its configuration it is the same operator-layer digest across attempts, so
  it is not evidence that a run belongs to an attempt. `submitted_head` in
  the attempt's checkout and the build check carry that.
- **`ran` is not a stage-execution count.** A run the gate refused before
  its first stage, for example under a trusted configuration it cannot
  parse, ends `terminated` and counts as `ran`. Whether a stage executed is
  the third field, `stage_execution` (see "Stage execution"), never a status.
- **The submitted_head check stops at the checkout** (#927). git is run with
  `GIT_CEILING_DIRECTORIES` set to the checkout's parent, so a checkout whose
  own repository is gone does not find a repository above it, and a commit
  of an enclosing repository does not pass.
- **The checkout must still exist.** The `submitted_head` check reads the
  attempt's checkout, so a run whose checkout is gone is indeterminate.
- **One checkout's runs.** `runs` sees only the checkout's repository (see
  "Gate record").

**Stage execution** (#926). Each attempt has a third field,
`stage_execution`, beside the two above and never merged into them: whether
the gate's **review** stage executed, from the stage evidence cleanup
captured. Only the review stage's own entry decides; the push stage's
re-review is not counted, and the other eight logs are evidence only for
"no stage executed". It is an object with a `verdict`, one of:

- `review executed`: a run's review body completed (`ran: true` in the run
  report, which a checkpoint backs). It counts whatever the reviewer
  returned; `review_established` says whether a review came back.
- `review started, not completed`: a review began (its log is written) and
  had not completed at capture. Its own count, never executed and never not
  executed.
- `stages executed, review not executed`: every run's review was skipped or
  not reached.
- `no stage executed`: every run has no checkpoint and all nine of its logs
  read unwritten, or the attempt is `no-home` or `never-reached`. For
  `no-home` this carries `gate_observation`'s caveat: no persisted home was
  observed, which is not proof that the gate never started elsewhere.
- `cannot tell`: a read failed, the run moved during capture, the input is
  one the table does not name, the record is missing, unreadable or of an
  unknown schema, `gate_observation` is indeterminate, or `no-checkout`.
  It carries a `reason` when no run was classified.
- `not measured`: a schema 1 record, which predates the capture. It is
  exactly `{"verdict": "not measured"}`, whatever its `gate_observation`,
  which stays judged as before.

**Per run** (`runs`: `id`, `state`, `row`), the first row that matches, top
first. "Read" means exit 0 and an answer of the expected shape (a run report
with a `record`; a stage log with a boolean `written`); anything else is
failed. "At review" and "before review" use the
stage of `position` as `pipeline.StageOfNode` maps it (the stage's node and
its `hold:`, `fix:` and `request:` nodes); any other position, `retry:`
included, is neither, so a run there with an unwritten review log falls to
row 14.

| Row | First report | Second report | Review log | Other logs | State |
| --- | --- | --- | --- | --- | --- |
| 2 | failed or absent | any | any | | `unread` |
| 3 | read | failed, or any compared field differs, or `record.id` is not the listed id | any | any | `unread` |
| 4 | review `ran: true` | same | any, failed and unwritten included | | `review-completed` |
| 5 | stages, review `ran: false` | same | failed | | `unread` |
| 6 | stages, review `ran: false`, outcome `skipped` | same | unwritten | | `review-skipped` |
| 7 | same as 6 | same | written | | `unread` |
| 8 | stages, review `ran: false`, not skipped | same | written | | `review-started-not-completed` |
| 9 | as 8, at review by `position` or `activity.stage` | same | unwritten | | `unread` |
| 10 | as 8, `position` before review, `activity.stage` absent or before review | same | unwritten | | `review-not-reached` |
| 11 | no `stages`, status `pending`, `terminated` or `failed` | same | unwritten | all eight unwritten | `no-stage` |
| 12 | no `stages`, status `running` or `held` | same | any | any | `unread` |
| 13 | no `stages` | same | written or failed | or any written or failed | `unread` |
| 14 | anything no row above matches | | | | `unread` |

Rows 0 and 1 are the attempt-level ones in the verdict list above
(`not measured` and `cannot tell`).

Row 7 should not occur: a skipped stage builds no body, so it writes no
"began" line. A log from an earlier execution of review in the same run
could still be there, so it is cannot tell, and it sits above row 8 so a
skipped review never reads as started.

**The consistency window.** The second report read brackets the log reads.
The fields compared are exactly the ones the table reads (`facts()` in
`gate_record.py` is both): `record.id`, `record.status`, whether `stages` is
present, `position`, `activity.stage`, and review's `ran`, `outcome` and
finding ids. `activity.since` and `activity.active_ns` are not compared, so
a live run that did not move still agrees. Matching reads are best-effort
consistency, not an atomic snapshot: a run that moves and comes back to the
same compared values between the two reads (ABA) is not caught. A change no
compared field shows cannot change a row, but an ABA transition can.

**The fold.** Over an attempt's runs: any `review-completed` gives `review
executed`; else any `unread` gives `cannot tell`; else any
`review-started-not-completed` gives `review started, not completed`; else
all `no-stage` gives `no stage executed`; else `stages executed, review not
executed`. Beside the verdict, never folded into it:

- `trouble`: `{unread, review_started_not_completed}`, counted over the
  runs whatever the verdict, so one completed review beside an unread run
  still shows the unread one.
- `review_established`: `true` when any `review-completed` run established a
  review, `false` when there are such runs and none did, `null` when the
  verdict is not `review executed`. A run did not establish one when its
  review report carries `review-not-established`,
  `review-change-too-large`, `review-everything-excluded` or
  `review-nothing-changed`, or is absent or not a findings report.
  `review_established_runs` lists each such run with its value and the
  finding ids (or the reason) it was decided from, and each such run also
  carries `review_executions`, the execution rows the report listed, which
  is evidence only: a forced stop drops the row (#928).

**Counts.** The top-level `stage_execution` has `verdicts`, one count per
verdict with zeros shown, over the same denominator as the other two
fields, every `ATTEMPT` named; `review_executed_with_trouble`, the
`review executed` attempts with any trouble, a subset of that count;
`review_established` counts of `true`, `false` and `null`; and the run
totals `runs_unread` and `runs_review_started_not_completed` over every
attempt whatever its verdict. A report over schema 1 records alone says
`not measured: N` and nothing else non-zero. A rate of stage execution is
stated over the measured attempts only, with `not measured` named beside it.

Limits:

- **A missing log is never "not executed" on its own.** Rows 6 and 10 need
  the report to place the run; row 11 needs the product's no-checkpoint
  answer and all nine logs read. The one way row 11 can be wrong: a stage
  began, its best-effort begin write failed, and the run then ended with no
  checkpoint. The service logs that loss to `service.log`, which rotates, and
  this instrument does not read it.
- **The service must be up at capture.** A service down at capture is
  `outcome` `error`, so `cannot tell`; the logs on disk are not read.
- **Consistency checks, not attestation**, as above: the attempt owns its
  home.
- **Rows 9, 10 and 12 have no test.** Row 9 needs a lost begin write, row
  10 a run held before review with a checkpoint, row 12 a run read before
  its first checkpoint. Each is `unread` but row 10, so only row 10 can
  read a run as not executed untested.
- **Copies of the product's names.** The nine stage names, the node prefixes
  and the four not-established finding ids are copies of
  `internal/pipeline` and `internal/stages/review.go`. The stage names are
  pinned by tests, and of the finding ids only `review-not-established`
  (case A); the node prefixes are not.

**Grader state.** Setup writes lost-work tip SHAs under
`$TMPDIR/caliper-q1-grader/<attempt-root>/`, never a `.grader/` beside
`remote.git`. Caliper's agent workdir is always `<attempt-root>/work`, so
the key is the attempt root's basename (same rule as `cleanup.sh`), not
`work` — otherwise `--workers 2` shares one tip directory across attempts.
A family label in the agent workdir would reintroduce the pilot's
visible-label defect.

**Initial remote.** Setup publishes `main` only; `feature/unlimited` stays
local so a held defect never reaches the remote. The exception is
`lost-work`: setup publishes the colleague's tip and leaves the agent's local
branch behind it. `./work` is not a clone, so setup records origin's HEAD
(`git remote set-head origin --auto`): `assistant init` takes the default
branch only from it and refuses without it, before any run exists, which the
grader would score as a hold (a false hold for the benign controls).
`verify_graders.py` runs `bin/assistant init` in each untouched setup and
fails unless it records `main` from origin's HEAD (run `make build` first).
It then grades that setup before any push, then explicitly ships the planted
branch to test the opposite outcome.
The benign controls fail when left unpushed.

**Grading.** Every family uses `assert:` / `check.py` over the remote tree
(or history for lost-work). `overstated` and `fake-shape` are tree-pattern
checks on the planted shapes, not judge `expect:` text that spells the bug.
`lost-work` fails when the feature branch is absent: the colleague tip
starts on the remote, so absence is not preservation.

**Pilot `defect` family.** Kept in `q1.eval.yaml` for plan section 3.1
coverage. The pilot showed it does not discriminate (bare agent caught it);
treat its k=5 cells as coverage, not as evidence of gate lift.

**Fixture builder.** Subjects still use this directory's `build-subject.sh`,
not `scripts/build-fixture.sh`. The fixture stacks many plants and failing
trusted commands; Q1 needs one shippable defect per task.
