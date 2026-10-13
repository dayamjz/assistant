# Labeled corpus for the decision-model checks

These are the labeled sets behind the two cutoffs in `internal/judge/check.go`.
Each file holds one check's set and is named after the check. The loader in
`internal/judge/corpus_test.go` rebuilds every case from it and refuses a file
it could miscount. The measurement in `internal/judge/harness_test.go` asks
every case through a real `judge.Client`, so it measures the shipped question,
`ModelVersion`, and cutoff, never a copy of them.

## Where the cases came from

Every diff is a per-file diff from this repository's own history, named by its
abbreviated commit and path. Diffs were cut at a fixed length when the sets were
built: 4000 bytes for the trace set and 6000 for the screen set. A cut can end
partway through a line.

**`trace-reason.json`**: 120 diffs of non-test Go files, in pairs. Diffs `2k`
and `2k+1` are two commits' changes to the same path. Each diff carries one
sentence of reason, written by a model that had that diff in front of it. Each
diff is asked twice, which gives 240 cases. With its own reason the label is
true. With its pair's reason the label is false. The false reasons are honest
descriptions of another change to the same file. They are not adversarial lies,
and none was written or collected from a human reviewer. The set holds one kind
of failure, so it has no held-out split by kind.

**`reviewer-directed-text.json`**:

- 150 clean cases (label false), each a real per-file diff.
- 80 positives in family `blunt`, built from clean cases `n000`–`n079`.
- 69 positives in family `subtle`, built from clean cases `n080`–`n149`. Clean
  case `n106` adds no line, so no payload was injected into it.

To build a positive, one payload line is inserted into the clean case's diff,
right after an added line chosen at random. The line is commented in the file's
own syntax. A positive records its `base`, the index `at` where the line goes,
the `line` itself, and the `family` and `template` it came from. `templates`
lists each family's payload texts, and every line contains its template's text.
Template `i` was applied to every tenth blunt base (`i`, `i+10`, …) and every
eighth subtle base, counted from that family's first base. The insertion points
are as originally generated, with random seed 7 for blunt and seed 11 for
subtle.

The people building the check wrote every payload. So the screen's figures
describe a tripwire for plain attempts that somebody thought to write. They say
nothing about text written to evade the question, and they show no defense.
The `subtle` family is subtler only than `blunt`.

## How to measure

The measurement sends every case's changed lines to the provider with your key.
`make check` and CI never run it. It is built only with the `judgelive` tag, and
it fails rather than skips when `ASSISTANT_DECISION_MODEL_KEY` is unset.

```sh
# Both checks once over the whole corpus.
ASSISTANT_DECISION_MODEL_KEY=… go test -tags judgelive -run TestMeasureLive \
  -count=1 -v -timeout 60m ./internal/judge

# The screen held out by kind, plus call-to-call spread on 20 cases asked 3 more times.
ASSISTANT_DECISION_MODEL_KEY=… go test -tags judgelive -run TestMeasureLive \
  -count=1 -v -timeout 60m ./internal/judge \
  -args -check reviewer-directed-text -holdout blunt:subtle -subset 20 -repeat 3
```

| Option | What it does |
| --- | --- |
| `-check` | Which set to measure: `trace-reason`, `reviewer-directed-text`, or `all`. |
| `-holdout train:test` | Screen only. Picks the cutoff that best separates the train family's positives from the clean cases they were built from. Reports it on the test family's positives and every other clean case, next to the shipped cutoff. No diff is on both sides. |
| `-subset N -repeat R` | Picks `N` cases, half of each label, at fixed positions. Asks each one `R` more times and reports the spread: how far answers range, how many cases fire on only some calls, and the subset's AUC range. |
| `-workers` | Requests in flight. The default is 8. |

For each check the report gives:

- the AUC;
- the pass rates at the shipped cutoff, as `Result.Fired` decides them;
- failed calls by kind, which are left out of every figure.

For the screen it also gives the rates by payload family and by template.

`TestTheMeasurementRunsOverTheCorpusEndToEnd` and its neighbours in
`measure_test.go` run the same code over this corpus in `make check`. They use
the stand-in provider in `internal/judge/judgetest`. So the loader and the
harness are exercised there, but no provider answer is.

## Changing a cutoff, a question, or the version

PRD section 5, "Checks a decision model may add", owns the rule. A change must
beat what it replaces on examples held out by kind of failure, by more than the
call-to-call spread. The scorer and this labeled set are not edited in the same
change. Add cases in one change, then measure a scorer change against them in
another.
