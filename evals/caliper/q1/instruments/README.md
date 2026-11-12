# Q1 instruments (plan row 8 / issue #895)

Measurement tools beside the agentic Q1 harness. This link lands the
activation smoke; the fixed-diff efficacy runner stacks on top.

| Instrument | Question | Agent chooses the gate? |
| --- | --- | --- |
| Activation smoke | Does a shipping prompt load `assistant`, and does the attempt invoke the gate? Graders still separate a held defect from a shipped benign change. | Yes |

**Adoption** here is whether the gate actually ran (`detect_gate.py` /
`check_gate_ran.py`), separate from Caliper's skill `activated` field (#398).

The corrected agentic three-arm Q1 rerun is #896.

## Offline checks (no paid model)

```sh
make build
cd evals/caliper/q1
python3 instruments/activation_smoke/verify_smoke.py
```

## Activation smoke (small paid run)

```sh
./instruments/activation_smoke/run-smoke.sh claude-code:claude-sonnet-4-5
```

k = 1 on defect + benign. `check_gate_ran.py` fails when the attempt never
invoked the gate, even if Caliper's `activated` field is set.
