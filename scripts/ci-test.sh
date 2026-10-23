# The race suite in CI's test environment, as the check job's Test step runs
# it: bash scripts/ci-test.sh, from the repository root on a GitHub runner
# (it reads RUNNER_OS and RUNNER_TEMP, and on Linux needs passwordless sudo,
# unshare, setpriv and tini). Output goes to test.log in the current
# directory; the summary lines are printed, and the whole log is printed in
# a "full test output" group when the step fails, which is where
# scripts/flake-ledger.py reads failures from.
#
# What it runs, each with the check job's value as its default:
#   CI_TEST_PACKAGES  space-separated package patterns (./...)
#   CI_TEST_COUNT     go test -count, a positive integer (1)
#   CI_TEST_TIMEOUT   go test -timeout for each package binary (30m)
# A value it cannot use is refused before anything runs.
#
# The race detector is the point of this job, so it is not optional.
# The timeout is explicit because internal/service has run past go
# test's ten-minute default on a macOS runner, which fails the package
# with a panic and no assertion having failed.
# Verbose output goes to a file so a skipped test leaves a trace: a
# package that skipped its assertions still reads ok, and the step
# after this one lists every skip with its message.
# On Linux the tests run in a PID namespace of their own, as this
# runner's user, with tini as its first process so an orphan is reaped
# as it is on the host. tini stays root, so the namespace still holds
# one process whose working directory the tests cannot read: the
# tests that need a real refused read find it there, and it is the
# ancestor of every test, which the sweep sets aside. A sleep of the
# nobody user stands beside the tests for the ones that need another
# user's process off their parent chain, which the sweep sets aside
# because it shares no user and refuses a signal. The runner holds processes of its own user
# whose working directory cannot be read (its systemd user manager,
# (sd-pam) and a running sudo, #152), and agents.Sweeper refuses to
# clear while such a process runs, as P6 requires and the owner kept,
# so on the bare host every test asserting a clear sweep can only skip.
# In the namespace the process table is the test run's own, so those
# tests run, and ASSISTANT_REQUIRE_SWEEP_CLEAR turns a skip of theirs
# into a failure. macOS has no PID namespace and its runner holds the
# same kind of sudo, so there those tests skip naming it, and the
# Skipped tests step lists them: a green macOS lane establishes nothing
# about a relaunch or a sweep that has to clear.
# The terminal session provider's tests run against a real tmux, and
# ASSISTANT_REQUIRE_TMUX makes them fail rather than skip without one.

set -eo pipefail
count=${CI_TEST_COUNT:-1}
timeout=${CI_TEST_TIMEOUT:-30m}
read -r -a packages <<< "${CI_TEST_PACKAGES:-./...}"
if ! [[ $count =~ ^[1-9][0-9]*$ ]]; then
  echo "::error::CI_TEST_COUNT must be a positive integer, not '$count'"
  exit 2
fi
if ! [[ $timeout =~ ^[0-9]+[hms]$|^[0-9]+m[0-9]+s$|^[0-9]+h[0-9]+m$ ]]; then
  echo "::error::CI_TEST_TIMEOUT must be a duration such as 30m, not '$timeout'"
  exit 2
fi
if [ "${#packages[@]}" -eq 0 ]; then
  echo "::error::CI_TEST_PACKAGES names no package"
  exit 2
fi
echo "ci-test: go test -race -count=$count -timeout $timeout ${packages[*]}"
export ASSISTANT_REQUIRE_TMUX=1
run=()
if [ "$RUNNER_OS" = "Linux" ]; then
  export ASSISTANT_REQUIRE_SWEEP_CLEAR=1
  run=(sudo --preserve-env env "PATH=$PATH" "HOME=$HOME"
    unshare --pid --fork --mount-proc tini --
    bash -c 'setpriv --reuid=65534 --regid=65534 --clear-groups sleep infinity &
      uid=$1 gid=$2; shift 2
      exec setpriv --reuid="$uid" --regid="$gid" --init-groups "$@"' bash "$(id -u)" "$(id -g)")
else
  # macOS's own agents and xcrun write into the per-user temp dir
  # whatever TMPDIR says. With the tests' TMPDIR elsewhere, the
  # per-package check denies every package's processes writes
  # there, so the guard can set entries with Apple's names aside
  # there (run --per-package); it still watches both.
  TMPDIR=$(mktemp -d "$RUNNER_TEMP/tmp.XXXXXX")
  export TMPDIR
fi
# P15 per package: go test runs each package's test binary through
# `leak-check.py package` (-exec), which fails that package naming
# each process the binary started that is still running once it
# exits, then ends them, with other packages running beside it.
# Linux keeps a package's processes in its tree with the child
# subreaper, macOS by running the binary in a sandbox profile of
# its own that they all inherit; the script says what neither sees.
# Around the whole run, scripts/leak-check.py also fails naming
# every runner-user process and temp entry that is new when go test
# ends. Both snapshots are taken inside the namespace: a second one
# on the host after tini exits would miss what its teardown killed.
# The self-test first plants a detached process and a directory and
# must see this same guard, in this same wrapper, reject both, and
# the per-package check fail, name and end a planted process and
# leave one outside the package alone, so a guard that cannot see
# processes here fails the step instead of passing. Existing
# processes are matched by pid and start time, not by name; Go's
# build cache lives outside the watched temp roots. The only things
# set aside are taken for macOS's: Apple launchd jobs and XPC
# services only root can have defined, and Apple-named entries in
# the per-user temp dir, which no package can write; each is
# printed, and the script says on what evidence.
# Each package the check runs is recorded, and every package go test
# reports ok must be among them, so a lost -exec fails the step,
# and so does a run whose --per-package claim did not hold.
per_package=(-exec "python3 $PWD/scripts/leak-check.py package --")
export LEAK_CHECK_RECORD="$RUNNER_TEMP/per-package-checked.txt"
"${run[@]}" python3 scripts/leak-check.py self-test
: > "$LEAK_CHECK_RECORD"
"${run[@]}" python3 scripts/leak-check.py run --per-package -- \
  go test "${per_package[@]}" -race -count="$count" -timeout "$timeout" -v "${packages[@]}" > test.log 2>&1 || status=$?
passed=$(grep -cE '^ok[[:space:]]' test.log || true)
checked=$(wc -l < "$LEAK_CHECK_RECORD")
echo "leak-check: $checked package(s) ran through the per-package check; go test reported $passed ok"
if [ "$checked" -lt "$passed" ] || [ "$passed" -eq 0 ]; then
  echo "::error::fewer packages ran through the per-package check than passed"
  status=${status:-1}
fi
grep -E '^(ok|FAIL|--- FAIL|panic:|\?|LEAK|leak-check:)[[:space:]]' test.log || true
if [ -n "${status:-}" ]; then
  echo "::group::full test output"
  cat test.log
  echo "::endgroup::"
  exit "$status"
fi
