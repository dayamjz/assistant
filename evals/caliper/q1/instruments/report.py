#!/usr/bin/env python3
"""Report efficacy and adoption separately, as assigned and as run."""
from __future__ import annotations

from dataclasses import asdict, dataclass
from typing import Any

ARMS = ("ci_alone", "ci_plus_review", "full_gate")


@dataclass(frozen=True)
class AttemptRow:
    variant: str
    assigned_arm: str
    run_arm: str
    efficacy_ok: bool
    adoption_ok: bool
    gate_ran: bool
    detail: str = ""

    @property
    def assigned_matches_run(self) -> bool:
        return self.assigned_arm == self.run_arm


def _bucket() -> dict[str, dict[str, int]]:
    return {arm: {"ok": 0, "fail": 0, "n": 0} for arm in ARMS}


def summarize(rows: list[AttemptRow]) -> dict[str, Any]:
    """Four matrices: efficacy/adoption x as_assigned/as_run.

    as_assigned counts only rows whose assigned arm matches what ran.
    """
    ea, aa = _bucket(), _bucket()
    arms_run = (*ARMS, "unknown")
    er = {a: {"ok": 0, "fail": 0, "n": 0} for a in arms_run}
    ar = {a: {"ok": 0, "fail": 0, "n": 0} for a in arms_run}
    mismatches = 0
    for r in rows:
        if r.assigned_arm not in ARMS or r.run_arm not in arms_run:
            raise ValueError(f"bad arms assigned={r.assigned_arm!r} run={r.run_arm!r}")
        if not r.assigned_matches_run:
            mismatches += 1
        else:
            for table, ok in ((ea, r.efficacy_ok), (aa, r.adoption_ok)):
                cell = table[r.assigned_arm]
                cell["n"] += 1
                cell["ok" if ok else "fail"] += 1
        for table, ok in ((er, r.efficacy_ok), (ar, r.adoption_ok)):
            cell = table[r.run_arm]
            cell["n"] += 1
            cell["ok" if ok else "fail"] += 1
    return {
        "rows": [asdict(r) for r in rows],
        "mismatches_assigned_vs_run": mismatches,
        "efficacy_as_assigned": ea,
        "adoption_as_assigned": aa,
        "efficacy_as_run": er,
        "adoption_as_run": ar,
    }
