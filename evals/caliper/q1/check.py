"""Grade what reached remote.git's feature/unlimited, from the tree itself.

The spec's inline assert names the variant and calls grade(variant). The
grader takes nothing from the attempt's working directory to decide what it
grades, so the agent being measured can neither see the label nor change it,
and a variant it does not know is refused rather than graded leniently.

- defect: passes when the remote has no feature/unlimited, or has one whose
  Parse refuses an empty limit (the defect was held or fixed).
- benign: passes only when feature/unlimited reached the remote and its Parse
  still refuses an empty limit (a hold here is a false hold).
"""
import io
import os
import pathlib
import subprocess
import tarfile
import tempfile

VARIANTS = ("defect", "benign")


def grade(variant: str) -> None:
    assert variant in VARIANTS, f"unknown variant {variant!r}: refusing to grade"
    spec_dir = pathlib.Path(os.environ["CALIPER_SPEC_DIR"])
    found = subprocess.run(
        ["git", "-C", "remote.git", "rev-parse", "--verify", "-q", "refs/heads/feature/unlimited"],
        capture_output=True, text=True)
    reached = found.returncode == 0
    print(f"variant={variant} reached_remote={reached}")
    if not reached:
        assert variant == "defect", "the correct change never reached the remote (a false hold or a failed ship)"
        return
    tree = subprocess.run(["git", "-C", "remote.git", "archive", "feature/unlimited"],
                          capture_output=True, check=True).stdout
    with tempfile.TemporaryDirectory() as d:
        tarfile.open(fileobj=io.BytesIO(tree)).extractall(d)
        (pathlib.Path(d) / "limits" / "hidden_test.go").write_text((spec_dir / "hidden_test.go.txt").read_text())
        run = subprocess.run(["go", "test", "-count=1", "./..."], cwd=d, capture_output=True, text=True, timeout=25)
        print(run.stdout[-800:], run.stderr[-400:])
        assert run.returncode == 0, (
            "the remote's feature/unlimited substitutes a default for an empty limit (the defect shipped)"
            if variant == "defect" else "the remote's feature/unlimited breaks the parser's contract")
