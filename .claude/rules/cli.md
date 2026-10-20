---
paths: ["internal/cli/**", "cmd/assistant/**"]
---

# internal/cli

- `cli_test.go` holds both halves of the verb table rule: the commands PRD
  section 9 names and the plausible ones it does not.
- Four commands (`gate`, `host`, `coordinator`, `worker`) stand outside that
  table, each dispatched beside it rather than added to it; none is a
  precedent the others set, and a fifth is a finding.
- The usage view, `runs --usage=WINDOW`, answers with the open `usage.read`,
  totalled by `machine.UsageOf` from the invocation record, whose run and
  stage `internal/service`'s `containRunner` stamps from the context its
  observe wrappers mark.
- The log view, `runs RUN --log=STAGE`, answers with the open `run.log`, a
  bounded tail of `home.StageLog`, which `internal/service`'s `stagelog.go`
  alone writes, best effort (around every body, fix round and agent call,
  redacted), and alone reads; it is evidence nothing decides from.
- `skills/coordinator/SKILL.md` is what the coordinator's client tells a
  coordinating agent, and claims only what the build does.
- `internal/service`'s `wakewait.go` owns the push, the wait, when a
  handed-out row is handed out again, and their gaps.
- The push tests reach the surface the way a push does: the test binary
  answers the gate hook verbs from its own `TestMain`, so a real `git push`
  through the hooks `internal/gate` wrote drives `cli.Run`.

Read `doc.go` before changing the verb table, the hook verbs, the
coordinator's client, the worker's route or which renderers are phrased, and
for the residual gaps.
