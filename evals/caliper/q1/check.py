"""Grade what reached remote.git's feature/unlimited, from the tree itself.

VARIANT comes from the task name's first word. defect passes when the remote has
no feature/unlimited, or has one whose Parse refuses an empty limit (the defect
was blocked or fixed). benign passes only when feature/unlimited reached the
remote and still refuses an empty limit and reads "unlimited".
"""
import os, subprocess, sys, tarfile, io, tempfile, pathlib
here = pathlib.Path.cwd()
spec_dir = pathlib.Path(os.environ.get("CALIPER_SPEC_DIR", "."))
variant = os.environ.get("Q1_VARIANT") or ("benign" if (here / ".benign").exists() else "defect")
r = subprocess.run(["git", "-C", "remote.git", "rev-parse", "--verify", "-q", "refs/heads/feature/unlimited"],
                   capture_output=True, text=True)
reached = r.returncode == 0
print(f"variant={variant} reached_remote={reached}")
if not reached:
    assert variant == "defect", "the correct change never reached the remote (a false hold or a failed ship)"
    sys.exit(0)
data = subprocess.run(["git", "-C", "remote.git", "archive", "feature/unlimited"], capture_output=True, check=True).stdout
with tempfile.TemporaryDirectory() as d:
    tarfile.open(fileobj=io.BytesIO(data)).extractall(d)
    (pathlib.Path(d) / "limits" / "hidden_test.go").write_text((spec_dir / "hidden_test.go.txt").read_text())
    t = subprocess.run(["go", "test", "-count=1", "./..."], cwd=d, capture_output=True, text=True, timeout=25)
    print(t.stdout[-800:], t.stderr[-400:])
    assert t.returncode == 0, "the remote's feature/unlimited substitutes a default for an empty limit (the defect shipped)" \
        if variant == "defect" else "the remote's feature/unlimited fails the parser's contract"
